# bin/lib/handoff-prompt.sh: build the prompt the builder gets for one task.
#
# Sourced by bin/tandem-handoff, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-handoff sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# LAUNCH_PROMPT is filled by a sequence of global ${var//{{SLOT}}/value}
# replaces, so a value containing another slot's "{{SLOT}}" text would be
# rewritten by a later pass. Plan names and .tandem.json values are
# repo-controlled, so every value goes through defang_slots first.
#
# Nested braces ("{{{OVERRULED}}}") survive one pass, so this loops to a fixed
# point and refuses input that needs more than DEFANG_MAX_PASSES passes.
#
# defang_slots DEST_VAR VALUE writes with `printf -v`, because `$(...)` would
# strip the trailing newlines the template relies on.
SLOT_NAMES=(PLAN ROOT PLANNER PRELUDE CHECKLINE CHECK TASK TOTAL BRIEF LEDGER JUDGE EXTRA OVERRULED)
DEFANG_MAX_PASSES=64
defang_slots() {
  local __dest="$1" __v="$2" __prev name defanged __i=0
  while :; do
    __prev="$__v"
    for name in "${SLOT_NAMES[@]}"; do
      # The replacement goes through a plain variable. Writing its braces inline
      # inside ${...} leaves a stray backslash on this bash.
      defanged="{$name}"
      __v="${__v//\{\{$name\}\}/$defanged}"
    done
    [[ "$__v" == "$__prev" ]] && break
    __i=$((__i + 1))
    if (( __i > DEFANG_MAX_PASSES )); then
      die "the value for $__dest carries slot markers nested deeper than $DEFANG_MAX_PASSES passes can defang; refusing rather than risk an injected ruling. It begins: ${__v:0:60}"
    fi
  done
  printf -v "$__dest" '%s' "$__v"
}

# ==============================================================================
# LAUNCH_PROMPT: the contract handed to the builder for each task.
#
# The template is a quoted heredoc and values are substituted afterwards, so
# repo-supplied text such as prompt_extra is never evaluated by the shell.
# Project-specific wording belongs in .tandem.json prompt_extra. Defaults:
#   1. It does not commit unless the plan says to.
#   2. It runs the check after the task, through `tandem check`.
#   3. The brief outranks this prompt on conflict.
#   4. It reads the brief, never the whole plan.
#   5. It hands back through tandem-handback --task, then stops.
# ==============================================================================
# build_prompt: fill LAUNCH_PROMPT from bin/lib/handoff-prompt.txt.
build_prompt() {
  [[ -r "$HANDOFF_LIB/handoff-prompt.txt" ]] \
    || die "the prompt template $HANDOFF_LIB/handoff-prompt.txt is missing; re-run install.sh. Nothing was sent."
  read -r -d '' TEMPLATE < "$HANDOFF_LIB/handoff-prompt.txt" || true

  # Ask for the conventions file only when the CLI does not load it already.
  PRELUDE=""
  if [[ -n "$CONVENTIONS_FILE" ]]; then
    _loaded=0
    if [[ -n "$BUILDER_KIND" ]]; then
      while IFS= read -r _f; do
        [[ -n "$_f" && "$_f" == "$CONVENTIONS_FILE" ]] && _loaded=1
      done < <(tandem agent instruction-files "$BUILDER_KIND" 2>/dev/null || true)
    fi
    (( _loaded )) || PRELUDE="
Also read $ROOT/$CONVENTIONS_FILE in full before you start. It carries
constraints you cannot infer from the code.
"
  fi

  # What earlier tasks did, from the build record.
  LEDGER=""
  [[ -n "$LEDGER_TEXT" ]] && LEDGER="
Earlier tasks, as tandem recorded them. Files and check results are measured;
each note is the account of the conversation that did the task.

$LEDGER_TEXT
"

  # `tandem check` keeps the check's output in a log and prints only the result.
  if [[ -n "$CHECK_COMMAND" ]]; then
    CHECKLINE=" When it is done, run the project's check ($CHECK_COMMAND)
through tandem, which keeps the full output in a log and shows you only the
result:

    tandem check

If the conventions say the check needs extra arguments where you run, put them
after --, as in: tandem check -- ARGS"
    CHECK="the check (\`tandem check\`), "
  else
    CHECKLINE=""
    CHECK=""
  fi

  # The build owns the triage switch. A resume keeps its first handoff's choice.
  JUDGE=""
  if [[ "$B_JUDGE" == true ]]; then
    JUDGE="
Tandem decides when a task gets a review. With review triage on, it may skip
one.
"
  fi

  # Optional staffing advice, off unless HERDR_DECISION=1. Every failure leaves
  # ADVICE empty. `tandem decision` reads the builder from the relay record.
  ADVICE=""
  if (( CONTINUE )); then
    # Staffing advice is given with the first task only.
    info "staffing advice: not asked again for a later task."
  elif [[ "${HERDR_DECISION:-0}" == 1 ]]; then
    # --status tells "never asked" apart from "nothing to say". Line 1 is a
    # token, line 2 the reason.
    _st=$(mktemp) || _st=/dev/null
    ADVICE=$(tandem decision assess \
      --plan "$PLAN_ABS" --root "$ROOT" \
      --render --status "$_st" 2>/dev/null) || ADVICE=""
    { read -r _tok; read -r _why; } < "$_st" 2>/dev/null || true
    [[ "$_st" == /dev/null ]] || rm -f "$_st"
    case "${_tok:-}" in
      advice)    info "staffing advice: added to the launch prompt (Jev)." ;;
      no_advice) info "staffing advice: none (Jev answered but was not confident enough about this plan to advise)." ;;
      off)       info "WARNING staffing advice: HERDR_DECISION=1 but TYPESAFE_API_KEY is not set; Jev was not asked." ;;
      *)         info "WARNING staffing advice: the Jev call failed (${_why:-${_tok:-no answer}}); none added." ;;
    esac
  else
    info "staffing advice: off (HERDR_DECISION is not 1)."
  fi
  if [[ -n "$JUDGE" ]]; then
    info "review triage: tandem calls Jev after each task's check."
  elif [[ "$RESUMED" == true ]]; then
    info "review triage: off (stored build decision)."
  elif [[ "${HERDR_JUDGE:-0}" != 1 ]]; then
    info "review triage: off (HERDR_JUDGE is not 1)."
  else
    info "WARNING review triage: off (TYPESAFE_API_KEY is not set)."
  fi

  # prompt_extra comes last so the repo's own text has the final word.
  if [[ -n "$ADVICE" || -n "$PROMPT_EXTRA" ]]; then
    EXTRA="
$ADVICE$PROMPT_EXTRA
"
  else
    EXTRA=""
  fi

  # The ruling is the one exception to "the plan wins". The objection goes with
  # it so the builder knows which steps it covers.
  OVERRULED=""
  if [[ -n "$OVERRIDE_ID" ]]; then
    _obj=$(sed -n '1,40p' "$MAILBOX/reviews/$OVERRIDE_ID.md" 2>/dev/null \
           | sed 's/^/  /') || true
    OVERRULED="
OVERRULED OBJECTIONS
You raised these against this plan and the planner has ruled. These rulings
outrank the plan - they are the only part of this message that does. Do not
raise them again.

$_obj

  RULING: $OVERRIDE_REASON
"
  fi

  # Each value is defanged into its own _D_* variable. The originals are printed
  # to the human below and must stay unchanged.
  LAUNCH_PROMPT="$TEMPLATE"
  defang_slots _D_PLAN "$PLAN_ABS"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{PLAN\}\}/$_D_PLAN}"
  defang_slots _D_ROOT "$ROOT"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{ROOT\}\}/$_D_ROOT}"
  defang_slots _D_PLANNER "$PLANNER_NAME"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{PLANNER\}\}/$_D_PLANNER}"
  defang_slots _D_PRELUDE "$PRELUDE"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{PRELUDE\}\}/$_D_PRELUDE}"
  defang_slots _D_CHECKLINE "$CHECKLINE"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{CHECKLINE\}\}/$_D_CHECKLINE}"
  defang_slots _D_CHECK "$CHECK"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{CHECK\}\}/$_D_CHECK}"
  defang_slots _D_TASK "$TASK"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{TASK\}\}/$_D_TASK}"
  defang_slots _D_TOTAL "$TOTAL"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{TOTAL\}\}/$_D_TOTAL}"
  # The brief path and the ledger notes are repo-influenced, so defang them too.
  defang_slots _D_BRIEF "$BRIEF"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{BRIEF\}\}/$_D_BRIEF}"
  defang_slots _D_LEDGER "$LEDGER"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{LEDGER\}\}/$_D_LEDGER}"
  defang_slots _D_JUDGE "$JUDGE"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{JUDGE\}\}/$_D_JUDGE}"
  defang_slots _D_EXTRA "$EXTRA"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{EXTRA\}\}/$_D_EXTRA}"
  # OVERRULED goes last so no later pass can rewrite the ruling.
  defang_slots _D_OVERRULED "$OVERRULED"
  LAUNCH_PROMPT="${LAUNCH_PROMPT//\{\{OVERRULED\}\}/$_D_OVERRULED}"
}
