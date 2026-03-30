#!/bin/bash

FILE="${OPENCLAW_CONFIG_DIR}/devices/pending.json"
JQ=${OPENCLAW_CONFIG_DIR}/bin/jq

while sleep 2; do
	$JQ -r 'keys|.[]' $FILE 2>/dev/null |xargs -r -n 1 openclaw devices approve 
done


