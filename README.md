# workflows
Reuseable Workflows for testing and building the compliance-framework application.
Repository must be Private and accesible by other repositories in the Organization.

## Secrets
The workflow requires secret value `GH_DEPL_KEY_CONF_SVC`. This is the private key of the key pair setup as Deploy Key `workflow-key` in the Private repo `configuration-service`. It is required to checkout the repository. It can alternatively be setup as an Organization-Level Secret.