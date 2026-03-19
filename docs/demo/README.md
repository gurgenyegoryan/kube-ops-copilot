# Terminal Demo Assets

This directory contains terminal demo scripts for project walkthroughs.

They are designed for [VHS](https://github.com/charmbracelet/vhs), which can render terminal sessions to animated GIF or SVG files.

Suggested flows:

1. `diagnose-telemetry-rich.tape`
   Shows a telemetry-aware `diagnose` run with live activity feed.
2. `smart-remediate-approval.tape`
   Shows a `smart-remediate` run that produces a plan, requests approval, and explains the next step.

Example render commands:

```bash
vhs docs/demo/diagnose-telemetry-rich.tape
vhs docs/demo/smart-remediate-approval.tape
```

If you want GIF assets in the repo later, these tape files are the safest starting point because they are reproducible and easy to refresh as the CLI evolves.
