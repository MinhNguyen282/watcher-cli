Query the repository map, a pre-built index of the codebase's files,
symbols, and their relationships (call graphs, imports, interface
implementations). Use it instead of blind grepping when you need to
know what depends on what.

Queries:
- `blast_radius` — everything connected to a file, symbol, or package
  within N hops. Use before editing to understand the impact surface.
- `callers` — who calls a function or method.
- `callees` — what a function or method calls.
- `implementers` — which types implement an interface.
- `related` — files directly connected to a file.
- `summary` — the most-connected symbols in the repository.

The map is built from the repository on the first request and cached.
Go symbols are high-confidence (real parse); other languages are
low-confidence structural hints. Always verify with view/grep before
editing.
