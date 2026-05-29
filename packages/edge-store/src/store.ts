export type JsonValue =
  | null
  | boolean
  | number
  | string
  | JsonValue[]
  | { [key: string]: JsonValue };

export type JsonObject = { [key: string]: JsonValue };

export interface EdgeStoreClock {
  now(): Date;
}

export interface EdgeMigration {
  version: string;
  name: string;
  checksum: string;
  sql: string;
}

export interface EdgeMigrationRunner {
  appliedVersions(): Promise<ReadonlySet<string>>;
  apply(migration: EdgeMigration): Promise<void>;
  applyAll(migrations: readonly EdgeMigration[]): Promise<void>;
}
