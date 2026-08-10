#!/bin/sh
set -eu

if ! IFS= read -r request; then
	printf '%s\n' 'evaluator-always-clear: missing request' >&2
	exit 1
fi

case "$request" in
	*'"schema":"grd.evaluator-request/v1"'*) ;;
	*)
		printf '%s\n' 'evaluator-always-clear: unsupported request schema' >&2
		exit 1
		;;
esac

printf '%s\n' '{"schema":"grd.evaluator-result/v1","requiresAction":false,"reason":"The playground evaluator clears every policy.","evidence":["This is a deterministic plumbing demonstration, not a semantic review."],"provenance":{"evaluator":"example://always-clear","contractRevision":"example/v1"}}'
