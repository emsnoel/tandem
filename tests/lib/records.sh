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

# Read the binding named by the fake builder's prompt, even after a repair.
# These fixture paths contain no single quotes. The controller test exercises
# shell execution with spaces and quotes in the path.
builder_prompt_field() {
  local helper
  helper=$(printf '%s\n' "$1" | sed -n "s/^    '\([^']*\/builder\/[0-9a-f]*\)' --started$/\1/p" | head -1)
  [ -n "$helper" ] || return 1
  grep -o -- "--$2 [0-9a-f][0-9a-f]*" "$helper" | head -1 | cut -d' ' -f2
}
