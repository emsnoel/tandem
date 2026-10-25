# Sourced by tests/run.sh. Clears every feature switch so no fixture inherits
# one. A fixture that tests a switch sets it for itself.
unset HERDR_DECISION HERDR_JUDGE HERDR_TRIAGE_SKIP TYPESAFE_API_KEY
unset TANDEM_BUILD_ENGINE TANDEM_STATE_HOME

# Point Herdr's CLI at a socket that does not exist, so a call a fixture
# forgot to fake fails as unreachable instead of reaching real panes.
export HERDR_SOCKET_PATH="${TMPDIR:-/tmp}/tandem-suite-no-herdr/herdr.sock"

# Nor may a fixture inherit the suite's pane. One that needs a pane sets these.
unset HERDR_ENV HERDR_PANE_ID HERDR_TAB_ID HERDR_WORKSPACE_ID

# A prompt counts as delivered once its turn has run for 8 seconds
# (cmd/tandem/confirm_start.go). The fakes settle at once or never, so one
# second is enough to tell them apart.
export TANDEM_FAIL_WITHIN_MS=1000
