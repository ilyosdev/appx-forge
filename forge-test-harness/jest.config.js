/** Phase 30 chaos harness jest config. */
module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  testMatch: ['**/*.spec.ts'],
  // Chaos scenarios share a single docker-compose stack — running them
  // in parallel would step on each other. --runInBand at the npm-script
  // level enforces that, but pin maxWorkers here too as a belt-and-braces.
  maxWorkers: 1,
  // Long timeout for the smoke spec itself (its first run builds Go
  // images from scratch). Individual `it` blocks override as needed.
  testTimeout: 600_000,
};
