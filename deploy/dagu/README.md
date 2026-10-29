# Dagu app reporting adapter

`report_job.py` runs one existing Dagu step and routes its Ganclaw notifications
to the app. It requires Python 3.9 or newer and a Unix socket connection to the
gateway. No extra Python packages are needed.

Actual reports and routine execution receipts have different destinations:

- A notification sent by the step appears in its bot conversation, including
  normal unread and push behaviour. It is not tagged as a scheduled job.
- An agent result uses Ganclaw's reserved `background` tag so a named session
  cannot hide its reply in Jobs. Session, quiet, async and timeout options are
  preserved. An agent call without a delivery target remains return-only.
- The wrapper's completion or failure receipt is tagged with the scheduled job
  name and stays in Jobs. Only a failed receipt triggers a job-failure push.

Previously, tagging every notification with the scheduled job name caused the
server's Jobs-only filtering to hide useful reports from conversations and
suppress their normal notifications. The adapter deliberately applies the job
tag only to its own final receipt. Visible sends also use a separate idempotency
namespace so an older Jobs-only delivery cannot suppress a corrected delivery.

## Use from a Dagu step

Install the script at a location readable by the Dagu service user, then wrap
the original step command, for example:

```yaml
steps:
  - id: check_support
    run: >-
      python3 /opt/ganclaw/dagu/report_job.py
      --socket /run/ganclaw/ganclaw.sock
      --job support-check
      --agent support
      --step check_support
      --command-file /opt/automation/check-support.sh
```

The command file contains the original shell command and runs in the step's
working directory with its existing environment. `--socket` can instead come
from `GANCLAW_SOCKET`. The wrapper supplies its temporary proxy socket to child
processes through `GANCLAW_SOCKET`; CLI calls already honour this variable.
Scripts with a hardcoded socket must be changed to honour it too.

`--agent` is the fallback destination and owns the completion receipt. An
explicit `app:<agent>` notification retains its destination. For scripts that
select among legacy Telegram accounts, provide explicit mappings:

```text
--account-agent helpdesk=support --account-agent outreach=sales
```

Unmapped Telegram accounts use `--agent`; no real accounts or contact addresses
are built into the adapter. This intentionally moves those notifications into
the app rather than sending a Telegram copy.

Child stdout and stderr remain in Dagu's logs and are never copied into app
receipts. The wrapper preserves child failure exit codes and reports a failure
if its final receipt cannot be delivered. It forwards termination signals to
the child process group. It does not replay missed reports, retry requests, or
cancel agent tasks already accepted by the gateway.

## Test

```sh
python3 -m unittest discover -s deploy/dagu -v
```

The integration tests run the wrapper against a fake Unix-socket gateway. They
verify that the actual report and the final Jobs receipt remain separate, no
raw stdout leaks into the receipt, and failed steps retain their exit code.
