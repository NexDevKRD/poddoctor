# Security Policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, report it privately via [GitHub Security Advisories](https://github.com/chenar/poddoctor/security/advisories/new) for this repository, or email **chenar.breshk@gmail.com** with:

- A description of the vulnerability and its impact.
- Steps to reproduce (a minimal manifest/config is ideal).
- The PodDoctor version and Kubernetes version affected.

You should get an initial response within 5 business days.

## Supported versions

Only the latest tagged release receives security fixes. There is no LTS branch.

## Scope

PodDoctor's threat model: it runs with read-only cluster-wide access to Pods/Events/ReplicaSets/Deployments/ControllerRevisions, write access limited to its own `PodDiagnosis` CRD and Events, and **no access to Secrets**. In-scope issues include anything that would let PodDoctor (or a compromised PodDoctor pod) read/write beyond that RBAC surface, or that would let a diagnosed Pod's log/event content lead to code execution or injection in the controller, dashboard, or webhook payload.
