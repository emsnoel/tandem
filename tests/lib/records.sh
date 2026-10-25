# tests/lib/records.sh: write the relay records bin/tandem-pair writes, whole,
# so a suite cannot pass by omitting a field. Suites that test broken records
# write them by hand.

# write_reviewer DIR [kind] [name] [model]: DIR/reviewer.json, kind claude and
# name reviewer by default. With no model, the model is unmeasured.
write_reviewer() {
  local dir="$1" kind="${2:-claude}" name="${3:-reviewer}" model="${4:-}"
  mkdir -p "$dir"
  jq -cn --arg k "$kind" --arg n "$name" --arg m "$model" \
    '{kind:$k, name:$n, model:$m, autonomy:"auto", model_measured:($m != "")}' > "$dir/reviewer.json"
}

# write_builder DIR [kind] [autonomy]: DIR/builder.json, kind codex and
# autonomy auto by default, not adopted.
write_builder() {
  local dir="$1" kind="${2:-codex}" autonomy="${3:-auto}"
  mkdir -p "$dir"
  jq -cn --arg k "$kind" --arg a "$autonomy" '{kind:$k, autonomy:$a, adopted:false}' > "$dir/builder.json"
}
