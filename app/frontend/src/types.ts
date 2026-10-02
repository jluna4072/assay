export interface Rule {
  id: string;
  criterion: string;
  kind: "file_exists";
  path: string;
  dependsOn: string[] | null;
}
export interface Contract {
  id: string;
  name: string;
  rules: Rule[] | null;
}
export interface PublishedContract {
  contract: Contract;
  version: number;
  publishedAt: string;
}
export type ResultStatus = "passed" | "failed" | "error" | "blocked";
export interface RuleResult {
  ruleId: string;
  passed: boolean;
  reason: string;
  status: ResultStatus;
}
export interface Attempt {
  number: number;
  startedAt: string;
  completedAt: string;
  status: ResultStatus;
  results: RuleResult[];
}
export interface Run {
  id: string;
  contractId: string;
  version: number;
  projectDir: string;
  status: ResultStatus | "ready";
  createdAt: string;
  attempts: Attempt[];
}
export interface DesktopAPI {
  ListContracts(): Promise<Contract[]>;
  SaveDraft(contract: Contract): Promise<Contract>;
  GetDraft(id: string): Promise<Contract>;
  PublishContract(id: string): Promise<PublishedContract>;
  ListVersions(id: string): Promise<PublishedContract[]>;
  GetVersion(id: string, version: number): Promise<PublishedContract>;
  SelectProject(): Promise<string>;
  StartRun(id: string, version: number, path: string): Promise<Run>;
  EvaluateAttempt(id: string): Promise<Run>;
  GetRun(id: string): Promise<Run>;
  ListRuns(): Promise<Run[]>;
}
declare global {
  interface Window {
    go?: { main: { Desktop: DesktopAPI } };
  }
}
