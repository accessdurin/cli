export interface PendingCliCall {
  readonly idempotencyKey: string;
  readonly approvalId?: string | undefined;
}
export interface CliCallLedger {
  read(fingerprint: string): Promise<PendingCliCall | null>;
  save(fingerprint: string, call: PendingCliCall): Promise<void>;
  remove(fingerprint: string): Promise<void>;
}
