#!/bin/bash
# This script translates input text representing graphs in the `digraph` format
# (https://pkg.go.dev/golang.org/x/tools/cmd/digraph), such as the one
# generated by `ci-operator --print-graph`, to Graphviz.
#
# Arguments, if present, are forwarded to `dot`.  The default is to generate a
# PNG image.
#
# Example usage:
#
#     $ ci-operator … --print-graph | hack/graphviz.sh > out.png
#     $ ci-operator … --print-graph | hack/graphviz.sh | $image_viewer -
set -euo pipefail

awk_prog="$(cat <<'EOF'
BEGIN {
    print "digraph test {\nrankdir=LR;"
}

!/INFO|WARN/ {
    print "\"" $1 "\" -> \"" $2 "\""
}

END {
    print "}"
}
EOF
)"

[[ "$#" -eq 0 ]] && set -- -T png
awk "$awk_prog" | dot "$@"
