# Security Policy

## Reporting

Do not open public issues for suspected vulnerabilities involving credentials,
authorization bypass, tenant isolation, arbitrary command execution, or data
exposure.

Report security concerns privately through GitHub's security advisory feature.

## Security Principles

- Deny by default.
- Separate proposal, authorization, execution, and validation.
- Scope every run to an explicit repository, tenant, write set, and budget.
- Never trust model output as executable authority.
- Use isolated worktrees and process identities.
- Store secrets outside repositories and agent prompts.
- Emit immutable audit events for privileged decisions.
- Keep retrieval systems outside the transactional authority path.

## Supported Versions

Until the first stable release, only the latest commit on `main` receives
security fixes.

