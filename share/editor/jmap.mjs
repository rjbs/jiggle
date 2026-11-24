// A small JMAP client (RFC 8620): just what jiggle's editor needs of one.
// -- claude, 2026-11-23

const CORE = "urn:ietf:params:jmap:core";

// A method that failed: type is the JMAP error type, like "stateMismatch".
export class MethodError extends Error {
  constructor(name, error) {
    super(`${name}: ${error.description || error.type}`);
    this.type = error.type;
  }
}

export class Client {
  // Connects to the server the page came from, for the one account that
  // has the given capability.
  static async connect(capability) {
    const res = await fetch("/.well-known/jmap");
    if (!res.ok) throw new Error(`no JMAP session: ${res.status}`);
    const session = await res.json();
    const accountId = session.primaryAccounts[capability];
    if (!accountId) throw new Error(`the server has no account for ${capability}`);
    return new Client(session, accountId, capability);
  }

  constructor(session, accountId, capability) {
    this.session = session;
    this.accountId = accountId;
    this.using = [ CORE, capability ];
    const core = session.capabilities[CORE];
    this.maxObjectsInGet = core.maxObjectsInGet;
    this.maxObjectsInSet = core.maxObjectsInSet;
    this.maxCallsInRequest = core.maxCallsInRequest;
  }

  // Makes one request of several method calls, each [name, arguments], to
  // which the account's id is added.  It returns the responses' arguments,
  // in order.  A method that fails gives a MethodError in its place and
  // nothing is thrown for it, unless every is true, the default, which
  // throws the first.
  async request(calls, { every = true } = {}) {
    const res = await fetch(this.session.apiUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        using: this.using,
        methodCalls: calls.map(([name, args], i) => [ name, { accountId: this.accountId, ...args }, `c${i}` ]),
      }),
    });
    if (!res.ok) {
      const problem = await res.json().catch(() => ({}));
      throw new Error(problem.detail || `the server answered ${res.status}`);
    }
    const { methodResponses } = await res.json();

    const out = calls.map(([name]) => new MethodError(name, { type: "serverFail", description: "no response" }));
    for (const [name, args, callId] of methodResponses) {
      const i = Number(callId.slice(1));
      out[i] = name === "error" ? new MethodError(calls[i][0], args) : args;
    }
    if (every) for (const result of out) if (result instanceof MethodError) throw result;
    return out;
  }

  // Calls one method, and returns its response's arguments.
  async call(name, args) {
    return (await this.request([ [ name, args ] ]))[0];
  }

  // Gets objects by id, however many, with as many requests as that
  // takes.  It returns { list, state }: the state is that of the first
  // response, so that a /changes from it misses nothing.
  async getAll(type, ids, properties = null) {
    const calls = [];
    for (let i = 0; i < ids.length; i += this.maxObjectsInGet) {
      calls.push([ `${type}/get`, { ids: ids.slice(i, i + this.maxObjectsInGet), properties } ]);
    }
    const out = { list: [], state: null };
    for (let i = 0; i < calls.length; i += this.maxCallsInRequest) {
      for (const got of await this.request(calls.slice(i, i + this.maxCallsInRequest))) {
        out.state ??= got.state;
        out.list.push(...got.list);
      }
    }
    return out;
  }

  // The URL a blob is downloaded from.
  blobUrl(blobId, name, type) {
    return this.session.downloadUrl
      .replace("{accountId}", encodeURIComponent(this.accountId))
      .replace("{blobId}", encodeURIComponent(blobId))
      .replace("{name}", encodeURIComponent(name))
      .replace("{type}", encodeURIComponent(type));
  }

  // Calls heard with the account's states, an object of type to state,
  // when the connection is made and whenever a state changes after.  The
  // browser makes the connection again when it's lost.
  watch(heard) {
    const url = this.session.eventSourceUrl
      .replace("{types}", "*").replace("{closeafter}", "no").replace("{ping}", "30");
    const source = new EventSource(url);
    source.addEventListener("state", (ev) => {
      const changed = JSON.parse(ev.data).changed[this.accountId];
      if (changed) heard(changed);
    });
    return source;
  }
}
