-- Phase 11: approvals and capabilities moved to Sentinel Guard. Each audited remediation links to the
-- guard request that authorized it (its signed audit chain holds who approved it and when it ran).
ALTER TABLE remediation_audit ADD COLUMN guard_request TEXT;
