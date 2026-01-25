# Privacy Policy for Graft

**Effective Date:** December 23, 2025

Graft is built with a **privacy-first, zero-telemetry** architecture. As an agentless deployment tool, it is designed to ensure that your infrastructure data remains entirely private and under your control.

## 1. No Third-Party Communication
Graft does **not** communicate with any third-party APIs, analytics services, or external trackers. There are no "phone-home" features, usage statistics collection, or automated crash reporting sent to the developers or any third-party entities.

## 2. Direct One-to-One Communication
Graft operates exclusively through **direct, encrypted communication** between:
* **Your Local Machine:** Where the Graft binary is executed.
* **Your Remote Server(s):** The target infrastructure you define.

All deployment instructions, Docker configurations, and Traefik setups are transmitted directly over your established **SSH connection**. No intermediary servers or "middle-man" agents are involved in the process.

## 3. Data Handling
* **No Cloud Storage:** There is no "Graft Cloud" or central database. We do not store your server IP addresses, credentials, or deployment logs.
* **Local Configuration:** Any configuration profiles or session data created by Graft are stored locally on your machine. This data never leaves your environment except when transmitted to your own servers during a deployment.

## 4. User Responsibility
Since Graft does not use server-side agents, the security of your deployments relies on the integrity of your local machine and your SSH keys. We recommend following standard security practices for managing your private keys and server access.

---
*For questions regarding this policy, please open an issue on the [Graft GitHub Repository](https://github.com/the-graft-docs/Graft/issues).*
