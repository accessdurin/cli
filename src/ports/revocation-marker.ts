/** Records that logout kept a profile's grant because its revocation has not completed. */
export interface RevocationMarker {
  pending(): Promise<boolean>;
  mark(): Promise<void>;
  clear(): Promise<void>;
}
