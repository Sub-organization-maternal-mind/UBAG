// Lightweight gRPC client facade. The actual @grpc/grpc-js channel is created
// lazily so importing this module never requires the dependency at build time.

export interface UbagGrpcOptions {
  host: string;
  credentials?: unknown; // ChannelCredentials from @grpc/grpc-js when available
}

const GRPC_STATUS_MAP: Record<number, string> = {
  4: "UBAG-QUEUE-DEADLINE-001", // DEADLINE_EXCEEDED
  8: "UBAG-QUOTA-RESOURCE-EXHAUSTED-001", // RESOURCE_EXHAUSTED
  14: "UBAG-QUEUE-UNAVAILABLE-001", // UNAVAILABLE
  16: "UBAG-AUTH-UNAUTHENTICATED-001", // UNAUTHENTICATED
};

export function grpcStatusToUbagCode(status: number): string {
  return GRPC_STATUS_MAP[status] ?? "UBAG-INTERNAL-GRPC-001";
}

export class UbagGrpcClient {
  readonly host: string;
  private readonly credentials: unknown;

  constructor(options: UbagGrpcOptions) {
    this.host = options.host;
    this.credentials = options.credentials;
  }

  // createChannel lazily imports @grpc/grpc-js. Throws a clear error if the
  // optional dependency is not installed.
  async createChannel(): Promise<unknown> {
    try {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const grpc = await import("@grpc/grpc-js" as string) as {
        credentials: { createInsecure(): unknown };
        Client: new (host: string, creds: unknown) => unknown;
      };
      const creds = this.credentials ?? grpc.credentials.createInsecure();
      return new grpc.Client(this.host, creds);
    } catch {
      throw new Error(
        "UbagGrpcClient requires the optional '@grpc/grpc-js' dependency. Install it to use gRPC.",
      );
    }
  }
}
