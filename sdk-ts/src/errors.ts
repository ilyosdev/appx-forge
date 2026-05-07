// Error classes for Forge control plane API (RFC 7807 problem+json)
import type { ForgeErrorResponse } from './types.js';

/**
 * Base error for all Forge API errors.
 * Contains the RFC 7807 problem details from the response.
 */
export class ForgeError extends Error {
  public readonly problem: ForgeErrorResponse;

  constructor(problem: ForgeErrorResponse) {
    super(problem.detail ?? problem.title);
    this.name = 'ForgeError';
    this.problem = problem;
  }
}

/** Thrown when a resource is not found (HTTP 404). */
export class ForgeNotFoundError extends ForgeError {
  constructor(problem: ForgeErrorResponse) {
    super(problem);
    this.name = 'ForgeNotFoundError';
  }
}

/** Thrown on resource conflict (HTTP 409), e.g., duplicate app_name. */
export class ForgeConflictError extends ForgeError {
  constructor(problem: ForgeErrorResponse) {
    super(problem);
    this.name = 'ForgeConflictError';
  }
}

/** Thrown when the service is unavailable (HTTP 503), e.g., no nodes available. */
export class ForgeServiceError extends ForgeError {
  constructor(problem: ForgeErrorResponse) {
    super(problem);
    this.name = 'ForgeServiceError';
  }
}
