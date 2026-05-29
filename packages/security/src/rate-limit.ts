export type RateLimitScope = "tenant" | "app" | "device" | "actor" | "target" | "global";
export type RateLimitDecisionStatus = "allowed" | "limited";

export interface RateLimitPolicy {
  id: string;
  scope: RateLimitScope;
  limit: number;
  windowSeconds: number;
  burst?: number;
  action?: string;
}

export interface RateLimitUsage {
  used: number;
  resetAt: string;
}

export interface RateLimitDecision {
  status: RateLimitDecisionStatus;
  allowed: boolean;
  retryAfterSeconds?: number;
  remaining: number;
  resetAt: string;
  auditEventName: "rate_limit.allowed" | "rate_limit.rejected";
}

export function evaluateRateLimit(policy: RateLimitPolicy, usage: RateLimitUsage, now = new Date()): RateLimitDecision {
  validateRateLimitPolicy(policy);

  const effectiveLimit = policy.limit + (policy.burst ?? 0);
  const resetAtMs = Date.parse(usage.resetAt);
  const resetAt = Number.isFinite(resetAtMs) ? new Date(resetAtMs) : new Date(now.getTime() + policy.windowSeconds * 1000);
  const remaining = Math.max(effectiveLimit - usage.used, 0);

  if (usage.used < effectiveLimit) {
    return {
      status: "allowed",
      allowed: true,
      remaining: Math.max(effectiveLimit - usage.used - 1, 0),
      resetAt: resetAt.toISOString(),
      auditEventName: "rate_limit.allowed"
    };
  }

  return {
    status: "limited",
    allowed: false,
    retryAfterSeconds: Math.max(Math.ceil((resetAt.getTime() - now.getTime()) / 1000), 0),
    remaining,
    resetAt: resetAt.toISOString(),
    auditEventName: "rate_limit.rejected"
  };
}

export function validateRateLimitPolicy(policy: RateLimitPolicy): void {
  if (!policy.id.trim()) {
    throw new Error("rate limit policy id is required");
  }
  if (!Number.isInteger(policy.limit) || policy.limit < 1) {
    throw new Error("rate limit policy limit must be a positive integer");
  }
  if (!Number.isInteger(policy.windowSeconds) || policy.windowSeconds < 1) {
    throw new Error("rate limit policy windowSeconds must be a positive integer");
  }
  if (policy.burst !== undefined && (!Number.isInteger(policy.burst) || policy.burst < 0)) {
    throw new Error("rate limit policy burst must be a non-negative integer");
  }
}
