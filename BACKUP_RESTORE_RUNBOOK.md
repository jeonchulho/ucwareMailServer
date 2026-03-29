# Backup and Restore Runbook

This runbook defines the minimal backup and restore procedure for the current docker-compose deployment.

## Scope

Included backup targets:

- Application data directory: data/
- Generated config files: generated/
- Archive DB logical dump (PostgreSQL): archive-db.sql.gz
- Mail volume snapshot: /var/mail/vhosts (optional)
- Postfix spool snapshot: /var/spool/postfix (optional)

## Prerequisites

- Docker and Docker Compose available
- Compose services are healthy
- Sufficient local disk space for tar.gz + SQL dump

## Create Backup

Run:

```bash
chmod +x scripts/backup.sh scripts/restore.sh
./scripts/backup.sh
```

Example with custom output directory:

```bash
./scripts/backup.sh --out-dir /var/backups/ucware-mail
```

Example skipping large volume snapshots:

```bash
./scripts/backup.sh --skip-vmail --skip-postfix-spool
```

Result example:

- backups/20260329-145000/app-data.tar.gz
- backups/20260329-145000/generated.tar.gz
- backups/20260329-145000/archive-db.sql.gz
- backups/20260329-145000/vmail.tar.gz
- backups/20260329-145000/postfix-spool.tar.gz
- backups/20260329-145000/manifest.txt

## Restore Backup

Run:

```bash
./scripts/restore.sh --backup-dir backups/20260329-145000
```

Restore without compose restart:

```bash
./scripts/restore.sh --backup-dir backups/20260329-145000 --no-restart
```

## Recommended Recovery Drill (Weekly)

1. Create fresh backup using scripts/backup.sh.
2. Stop and restart stack in isolated test environment.
3. Run scripts/restore.sh against that backup.
4. Validate:
   - API login works
   - mailbox listing works
   - recent messages are visible
   - sending still works
5. Record elapsed restore time (RTO) and data gap (RPO).
6. Save drill result and remediation items.

## Safety Notes

- Restore is destructive for data/, generated/, and in-container volume contents.
- Always test restore in staging before production.
- Keep at least one off-host encrypted copy of backups.
