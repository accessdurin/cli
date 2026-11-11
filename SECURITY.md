# Reporting a vulnerability

Send confidential vulnerability reports to **support@getdurin.com**. Describe the
affected CLI version or commit, OS, expected boundary, and minimal reproduction
using synthetic data. Include a suggested fix if you have one.

Do not disclose credentials, customer tool payloads, or an unpatched exploit in a
public issue. Maintainers can coordinate reproduction and disclosure privately.
For ordinary bugs and feature requests, use
[GitHub issues](https://github.com/accessdurin/cli/issues).

Relevant boundaries include organization/profile binding, authorization host
validation, credential storage, operation and approval identity, local hooks, and
runtime/configuration file ownership. The [README](README.md#product-boundaries)
describes the CLI, gateway, and Endpoint responsibilities.
