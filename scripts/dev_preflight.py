#!/usr/bin/env python3
"""Read-only development API checks through the real Terraform provider.

No managed resources, payments, or infrastructure mutations are configured.
Credentials are read from an ignored local JSON file and passed only via the
subprocess environment. Temporary Terraform state is deleted on exit.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--credentials", type=Path, default=ROOT / ".ibee-dev.json")
    args = parser.parse_args()
    try:
        credentials = json.loads(args.credentials.read_text())
    except (OSError, ValueError):
        raise SystemExit("Create the ignored .ibee-dev.json with token and workspace_id before running this check.")
    if not isinstance(credentials, dict):
        raise SystemExit("Credentials must be a JSON object.")
    for key in ("token", "workspace_id"):
        if not isinstance(credentials.get(key), str) or not credentials[key].strip():
            raise SystemExit(f"Credentials require a nonempty {key} string.")
    token = credentials["token"].strip()
    if token.startswith("ibee_prod_key_"):
        raise SystemExit("Use a development API token for this check.")
    env = {k: v for k, v in os.environ.items() if not k.startswith(("IBEE_", "TF_"))}
    env.update(IBEE_TOKEN=token, IBEE_WORKSPACE_ID=credentials["workspace_id"].strip(),
               IBEE_ENDPOINT="https://api.ibee.co.in/v1", TF_IN_AUTOMATION="1", CHECKPOINT_DISABLE="1")
    if credentials.get("organization_id"):
        env["IBEE_ORGANIZATION_ID"] = str(credentials["organization_id"])
    with tempfile.TemporaryDirectory(prefix="ibee-dev-preflight-") as temp:
        work = Path(temp)
        binary = work / "bin" / "terraform-provider-ibee"
        binary.parent.mkdir()
        build = subprocess.run(["go", "build", "-buildvcs=false", "-o", str(binary), "."], cwd=ROOT, env=env, capture_output=True, text=True)
        if build.returncode:
            raise SystemExit("Provider build failed; run make build to inspect build diagnostics.")
        cli = work / "dev.tfrc"
        cli.write_text('provider_installation {\n dev_overrides { "devs-ibee/ibee" = ' + json.dumps(str(binary.parent)) + ' }\n direct {}\n}\n')
        env["TF_CLI_CONFIG_FILE"] = str(cli)
        (work / "main.tf").write_text('''terraform {
  required_version = ">= 1.11.0"
  required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {}
data "ibee_billing_eligibility" "account" {}
data "ibee_sites" "available" {}
data "ibee_images" "cloud" { vm_type = "cloud" }
data "ibee_compute_plans" "cloud" {
  vm_type = "cloud"
  currency = data.ibee_billing_eligibility.account.currency
}
output "preflight" {
  value = {
    billing_allowed = data.ibee_billing_eligibility.account.allowed
    billing_reason = data.ibee_billing_eligibility.account.reason
    currency = data.ibee_billing_eligibility.account.currency
    site_count = length(data.ibee_sites.available.sites)
    image_count = length(data.ibee_images.cloud.images)
    plan_count = length(data.ibee_compute_plans.cloud.plans)
  }
}
''')
        # Apply evaluates only data sources; this configuration has no resources.
        result = subprocess.run(["terraform", "apply", "-auto-approve", "-input=false", "-no-color"], cwd=work, env=env, capture_output=True, text=True)
        if result.returncode:
            diagnostic = (result.stdout + "\n" + result.stderr).replace(token, "[REDACTED]")
            # Print only the error section, not catalog/account state.
            index = diagnostic.find("Error:")
            print(diagnostic[index:] if index >= 0 else "Terraform preflight failed without a structured diagnostic.")
            raise SystemExit(1)
        result = subprocess.run(["terraform", "output", "-json", "preflight"], cwd=work, env=env, capture_output=True, text=True, check=True)
        print(json.dumps(json.loads(result.stdout), indent=2))
        print("Read-only development preflight passed. No infrastructure or payments were created.")


if __name__ == "__main__":
    main()
