# ADR-0003: Hybrid retrieval in Postgres (pgvector + full-text), fused with RRF

**Status:** accepted, 2026-10-05. Supersedes the OpenSearch plan in the original architecture.

## Context
On-call questions mix two kinds of query:
- **Paraphrases** ("pods keep dying from memory") that only dense embeddings match well.
- **Exact tokens** (`timeout acquiring DB connection`, `deployctl rollback`, metric names) that embeddings blur and lexical search nails.

We need both, isolated per tenant, and consistent with each other as documents are re-ingested.

## Decision
- Store chunk text, a `vector(384)` embedding and a generated, weighted `tsvector` (heading = A, body = B) **in the same row** of `knowledge_chunks`.
- Query both in one transaction with RLS bound to the caller's tenant. Lexical search ORs the query's stemmed lexemes (`websearch_to_tsquery` ANDs every term and almost never matches a natural-language question).
- Fuse with **Reciprocal Rank Fusion** (k = 60). It uses ranks only, so cosine similarity and `ts_rank_cd` don't need calibrating against each other.
- Embeddings: local ONNX `all-MiniLM-L6-v2` through Spring AI Transformers. No API key and no per-call cost, and the same model runs in the gateway (queries) and ingestion (documents).
- Ingestion is asynchronous over Kafka (`knowledge.documents`, keyed `tenant:docId`). Re-ingesting replaces all of a document's chunks atomically, unchanged content is skipped by hash, and poison messages go to a DLT.

## Why not OpenSearch (yet)
- One fewer stateful system (~800 MB container, separate backup, auth and tenancy model).
- No dual-write consistency problem: a chunk's vector and its text index change in the same transaction.
- Tenant isolation is enforced by the same Postgres RLS policies as everything else.
- At this corpus size (thousands of chunks per tenant), pgvector HNSW plus a GIN index answer in milliseconds.

**Revisit when** a tenant passes ~5M chunks, we need BM25 specifically (`ts_rank_cd` is not BM25), or we need language analyzers that Postgres lacks. The retrieval interface (`KnowledgeSearch`) hides the store, so swapping it doesn't touch callers.

## Consequences
- + Simple, consistent, cheap to run. The retrieval eval (`evals/retrieval`) measures vector vs. keyword vs. hybrid, so this is a claim we can check.
- − `ts_rank_cd` is weaker than BM25 on long documents. A cross-encoder reranker (planned) compensates more than switching engines would.
- − Changing the embedding model means re-embedding everything (dimension is fixed in the schema). Re-ingesting from Kafka (replay) makes that a routine operation.
