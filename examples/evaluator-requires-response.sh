#!/bin/sh
set -eu

if ! IFS= read -r request; then
	printf '%s\n' 'evaluator-requires-response: missing request' >&2
	exit 1
fi

case "$request" in
	*'"schema":"grd.evaluator-request/v1"'*) ;;
	*)
		printf '%s\n' 'evaluator-requires-response: unsupported request schema' >&2
		exit 1
		;;
esac

printf '%s\n' '{"schema":"grd.evaluator-result/v1","requiresAction":true,"reason":"The playground requires an explicit contributor Response.","evidence":["This deterministic evaluator exercises GRD requirement ownership and resumption."],"provenance":{"evaluator":"example://requires-response","contractRevision":"example/v1"}}'
