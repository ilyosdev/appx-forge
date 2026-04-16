// Stub errors -- implementation in GREEN phase
import type { ForgeErrorResponse } from './types.js';

export class ForgeError extends Error {
  constructor(public problem: ForgeErrorResponse) {
    super('not implemented');
  }
}

export class ForgeNotFoundError extends ForgeError {}
export class ForgeConflictError extends ForgeError {}
export class ForgeServiceError extends ForgeError {}
