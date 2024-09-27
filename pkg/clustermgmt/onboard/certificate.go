package onboard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/library-go/pkg/image/reference"

	"github.com/openshift/ci-tools/pkg/clustermgmt"
	citoolsyaml "github.com/openshift/ci-tools/pkg/util/yaml"
)

type certificateStep struct {
	log            *logrus.Entry
	clusterInstall *clustermgmt.ClusterInstall
	kubeClient     KubeClientGetter
	writeManifest  func(name string, data []byte, perm fs.FileMode) error
}

func (s *certificateStep) Name() string {
	return "certificate"
}

func (s *certificateStep) Run(ctx context.Context) error {
	log := s.log.WithField("step", s.Name())

	client, err := s.kubeClient()
	if err != nil {
		return fmt.Errorf("kube client: %w", err)
	}

	baseDomain, err := s.baseDomain(ctx, client)
	if err != nil {
		return fmt.Errorf("base domain: %w", err)
	}

	host, err := s.imageRegistryPublicHost(ctx, client)
	if err != nil {
		return fmt.Errorf("image registry public host: %w", err)
	}

	manifests := s.generateCertificateManifests(baseDomain, host)
	manifestMarshaled, err := citoolsyaml.MarshalMultidoc(yaml.Marshal, manifests...)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	outputPath := CertificateManifestPath(s.clusterInstall.Onboard.ReleaseRepo, s.clusterInstall.ClusterName)
	if err := s.writeManifest(outputPath, manifestMarshaled, 0644); err != nil {
		return fmt.Errorf("write template %s: %w", outputPath, err)
	}

	log.WithField("certificate", outputPath).Info("certificates generated")
	return nil
}

func (s *certificateStep) baseDomain(ctx context.Context, client ctrlruntimeclient.Client) (string, error) {
	if s.clusterInstall.Onboard.Certificate.BaseDomains != nil {
		if domain, ok := s.clusterInstall.Onboard.Certificate.BaseDomains[s.clusterInstall.ClusterName]; ok {
			s.log.Info("override base domain from config")
			return domain, nil
		}
	}

	cm := corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: "cluster-config-v1"}, &cm); err != nil {
		return "", fmt.Errorf("get cluster-config-v1: %w", err)
	}
	installConfigRaw, ok := cm.Data["install-config"]
	if !ok {
		return "", errors.New("install-config not found")
	}

	installConfig := struct {
		BaseDomain string `json:"baseDomain"`
	}{}
	if err := yaml.Unmarshal([]byte(installConfigRaw), &installConfig); err != nil {
		return "", fmt.Errorf("unmarshall install config: %w", err)
	}
	return installConfig.BaseDomain, nil
}

func (s *certificateStep) imageRegistryPublicHost(ctx context.Context, client ctrlruntimeclient.Client) (string, error) {
	if s.clusterInstall.Onboard.Certificate.ImageRegistryPublicHosts != nil {
		if publicHost, ok := s.clusterInstall.Onboard.Certificate.ImageRegistryPublicHosts[s.clusterInstall.ClusterName]; ok {
			s.log.Info("override image registry public host from config")
			return publicHost, nil
		}
	}

	isList := imagev1.ImageStreamList{}
	if err := client.List(ctx, &isList, &ctrlruntimeclient.ListOptions{Namespace: "openshift"}); err != nil {
		return "", fmt.Errorf("image streams: %w", err)
	}

	for i := range isList.Items {
		is := &isList.Items[i]
		if value := is.Status.PublicDockerImageRepository; len(value) > 0 {
			ref, err := reference.Parse(value)
			if err != nil {
				return "", fmt.Errorf("parse docker image repository: %w", err)
			}
			return ref.Registry, nil
		}
	}
	return "", fmt.Errorf("no public registry host could be located")
}

func (s *certificateStep) generateCertificateManifests(baseDomain, imageRegistryHost string) []interface{} {
	manifests := make([]interface{}, 0)

	projLabelKey, projLabelValue := s.projectLabelOrDefault("apiserver-tls", "aws-project", "openshift-ci-infra")
	apiServerCert := map[string]interface{}{
		"kind": "Certificate",
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				projLabelKey: projLabelValue,
			},
			"name":      "apiserver-tls",
			"namespace": "openshift-config",
		},
		"spec": map[string]interface{}{
			"dnsNames": []interface{}{
				fmt.Sprintf("api.%s.%s", s.clusterInstall.ClusterName, baseDomain),
			},
			"issuerRef": map[string]interface{}{
				"kind": "ClusterIssuer",
				"name": s.clusterIssuerOrDefault("apiserver-tls", "cert-issuer-aws"),
			},
			"secretName": "apiserver-tls",
		},
		"apiVersion": "cert-manager.io/v1",
	}

	projLabelKey, projLabelValue = s.projectLabelOrDefault("apps-tls", "aws-project", "openshift-ci-infra")
	appsCert := map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				projLabelKey: projLabelValue,
			},
			"name":      "apps-tls",
			"namespace": "openshift-ingress",
		},
		"spec": map[string]interface{}{
			"dnsNames": []interface{}{
				fmt.Sprintf("*.apps.%s.%s", s.clusterInstall.ClusterName, baseDomain),
			},
			"issuerRef": map[string]interface{}{
				"kind": "ClusterIssuer",
				"name": s.clusterIssuerOrDefault("apps-tls", "cert-issuer-aws"),
			},
			"secretName": "apps-tls",
		},
	}

	projLabelKey, projLabelValue = s.projectLabelOrDefault("registry-tls", "gcp-project", "openshift-ci-infra")
	imageRegistryCert := map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				projLabelKey: projLabelValue,
			},
			"name":      "registry-tls",
			"namespace": "openshift-image-registry",
		},
		"spec": map[string]interface{}{
			"dnsNames": []interface{}{
				imageRegistryHost,
			},
			"issuerRef": map[string]interface{}{
				"kind": "ClusterIssuer",
				"name": s.clusterIssuerOrDefault("registry-tls", "cert-issuer"),
			},
			"secretName": "public-route-tls",
		},
	}

	if !(*s.clusterInstall.Onboard.OSD || *s.clusterInstall.Onboard.Hosted || *s.clusterInstall.Onboard.Unmanaged) {
		manifests = append(manifests, apiServerCert, appsCert)
	}
	manifests = append(manifests, imageRegistryCert)

	return manifests
}

func (s *certificateStep) clusterIssuerOrDefault(certificate, def string) string {
	ci := s.clusterInstall.Onboard.Certificate.ClusterIssuer
	if cluster, ok := ci[s.clusterInstall.ClusterName]; ok {
		if clusterIssuer, ok := cluster[certificate]; ok {
			return clusterIssuer
		}
	}
	return def
}

func (s *certificateStep) projectLabelOrDefault(certificate, defKey, defValue string) (string, string) {
	ci := s.clusterInstall.Onboard.Certificate.ProjectLabel
	if projLabel, ok := ci[s.clusterInstall.ClusterName]; ok {
		if keyVal, ok := projLabel[certificate]; ok {
			return keyVal.Key, keyVal.Value
		}
	}
	return defKey, defValue
}

func NewCertificateStep(log *logrus.Entry, clusterInstall *clustermgmt.ClusterInstall,
	kubeClient KubeClientGetter) *certificateStep {
	return &certificateStep{
		log:            log,
		clusterInstall: clusterInstall,
		writeManifest:  os.WriteFile,
		kubeClient:     kubeClient,
	}
}
