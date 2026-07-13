--
-- PostgreSQL database dump
--

\restrict GKgwz0mQo1LQTCewMYkbGpvBvO4ne6nkmZlHTgLn4c8wGNcYoO9uMIoXU0oQxmF

-- Dumped from database version 17.10
-- Dumped by pg_dump version 17.10

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: vector; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;


--
-- Name: EXTENSION vector; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION vector IS 'vector data type and ivfflat and hnsw access methods';


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: agent_observation_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_observation_stats (
    agent_id text NOT NULL,
    observation_id bigint NOT NULL,
    injections integer DEFAULT 0 NOT NULL,
    successes integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: api_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    token_prefix text NOT NULL,
    scope text DEFAULT 'read-write'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    request_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    revoked boolean DEFAULT false NOT NULL,
    revoked_at timestamp with time zone
);


--
-- Name: concept_weights; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.concept_weights (
    concept text NOT NULL,
    updated_at text NOT NULL,
    weight real DEFAULT 0.1 NOT NULL
);


--
-- Name: content; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.content (
    hash text NOT NULL,
    doc text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: content_chunks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.content_chunks (
    hash text NOT NULL,
    seq integer NOT NULL,
    pos integer NOT NULL,
    model text NOT NULL,
    embedding public.vector(1536),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    text text DEFAULT ''::text NOT NULL
);


--
-- Name: documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documents (
    id bigint NOT NULL,
    collection text NOT NULL,
    path text NOT NULL,
    title text,
    hash text,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, ((COALESCE(path, ''::text) || ' '::text) || COALESCE(title, ''::text)))) STORED
);


--
-- Name: documents_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.documents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: documents_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.documents_id_seq OWNED BY public.documents.id;


--
-- Name: indexed_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.indexed_sessions (
    id text NOT NULL,
    workstation_id text NOT NULL,
    project_id text NOT NULL,
    project_path text,
    git_branch text,
    first_msg_at timestamp with time zone,
    last_msg_at timestamp with time zone,
    exchange_count integer DEFAULT 0,
    tool_counts jsonb,
    topics jsonb,
    content text,
    file_mtime timestamp with time zone,
    indexed_at timestamp with time zone DEFAULT now(),
    tsv tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, COALESCE(content, ''::text))) STORED
);


--
-- Name: injection_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.injection_log (
    id bigint NOT NULL,
    observation_id bigint NOT NULL,
    project text DEFAULT ''::text NOT NULL,
    task_context text DEFAULT ''::text NOT NULL,
    session_id text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    cited boolean DEFAULT false
);


--
-- Name: injection_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.injection_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: injection_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.injection_log_id_seq OWNED BY public.injection_log.id;


--
-- Name: invitations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.invitations (
    id integer NOT NULL,
    code character varying(64) NOT NULL,
    created_by integer NOT NULL,
    used_by integer,
    used_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: invitations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.invitations_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: invitations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.invitations_id_seq OWNED BY public.invitations.id;


--
-- Name: issue_comments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.issue_comments (
    id bigint NOT NULL,
    issue_id bigint NOT NULL,
    author_project text NOT NULL,
    author_agent text,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: issue_comments_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.issue_comments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: issue_comments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.issue_comments_id_seq OWNED BY public.issue_comments.id;


--
-- Name: issues; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.issues (
    id bigint NOT NULL,
    title text NOT NULL,
    body text,
    status text DEFAULT 'open'::text NOT NULL,
    priority text DEFAULT 'medium'::text NOT NULL,
    source_project text NOT NULL,
    target_project text NOT NULL,
    source_agent text,
    created_by_session text,
    labels jsonb DEFAULT '[]'::jsonb,
    acknowledged_at timestamp with time zone,
    resolved_at timestamp with time zone,
    reopened_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    type text DEFAULT 'task'::text NOT NULL,
    CONSTRAINT issues_priority_check CHECK ((priority = ANY (ARRAY['critical'::text, 'high'::text, 'medium'::text, 'low'::text]))),
    CONSTRAINT issues_status_check CHECK ((status = ANY (ARRAY['open'::text, 'acknowledged'::text, 'resolved'::text, 'reopened'::text, 'closed'::text, 'rejected'::text]))),
    CONSTRAINT issues_type_check CHECK ((type = ANY (ARRAY['bug'::text, 'feature'::text, 'improvement'::text, 'task'::text])))
);


--
-- Name: issues_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.issues_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: issues_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.issues_id_seq OWNED BY public.issues.id;


--
-- Name: migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.migrations (
    id character varying(255) NOT NULL
);


--
-- Name: observation_conflicts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.observation_conflicts (
    conflict_type text NOT NULL,
    resolution text NOT NULL,
    detected_at text NOT NULL,
    reason text,
    resolved_at text,
    id bigint NOT NULL,
    newer_obs_id bigint NOT NULL,
    older_obs_id bigint NOT NULL,
    detected_at_epoch bigint NOT NULL,
    resolved bigint DEFAULT 0,
    CONSTRAINT chk_observation_conflicts_conflict_type CHECK ((conflict_type = ANY (ARRAY['superseded'::text, 'contradicts'::text, 'outdated_pattern'::text]))),
    CONSTRAINT chk_observation_conflicts_resolution CHECK ((resolution = ANY (ARRAY['prefer_newer'::text, 'prefer_older'::text, 'manual'::text])))
);


--
-- Name: observation_conflicts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.observation_conflicts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: observation_conflicts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.observation_conflicts_id_seq OWNED BY public.observation_conflicts.id;


--
-- Name: observation_injections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.observation_injections (
    id bigint NOT NULL,
    observation_id bigint NOT NULL,
    session_id text NOT NULL,
    injection_section text NOT NULL,
    injected_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: observation_injections_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.observation_injections_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: observation_injections_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.observation_injections_id_seq OWNED BY public.observation_injections.id;


--
-- Name: observation_relations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.observation_relations (
    relation_type text NOT NULL,
    detection_source text NOT NULL,
    created_at text NOT NULL,
    reason text,
    id bigint NOT NULL,
    source_id bigint NOT NULL,
    target_id bigint NOT NULL,
    confidence real DEFAULT 0.5 NOT NULL,
    created_at_epoch bigint NOT NULL,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    CONSTRAINT chk_observation_relations_detection_source CHECK ((detection_source = ANY (ARRAY['file_overlap'::text, 'embedding_similarity'::text, 'temporal_proximity'::text, 'narrative_mention'::text, 'concept_overlap'::text, 'type_progression'::text, 'creative_association'::text]))),
    CONSTRAINT chk_observation_relations_relation_type CHECK ((relation_type = ANY (ARRAY['causes'::text, 'fixes'::text, 'supersedes'::text, 'depends_on'::text, 'relates_to'::text, 'evolves_from'::text, 'leads_to'::text, 'similar_to'::text, 'contradicts'::text, 'reinforces'::text, 'invalidated_by'::text, 'explains'::text, 'shares_theme'::text, 'parallel_context'::text, 'summarizes'::text, 'part_of'::text, 'prefers_over'::text, 'modifies'::text, 'reads'::text, 'follows'::text, 'prompted_by'::text, 'references'::text, 'referenced_by'::text])))
);


--
-- Name: observation_relations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.observation_relations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: observation_relations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.observation_relations_id_seq OWNED BY public.observation_relations.id;


--
-- Name: observation_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.observation_versions (
    id bigint NOT NULL,
    observation_id bigint NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    narrative text NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    source text DEFAULT 'original'::text NOT NULL
);


--
-- Name: observation_versions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.observation_versions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: observation_versions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.observation_versions_id_seq OWNED BY public.observation_versions.id;


--
-- Name: observations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.observations (
    file_mtimes text,
    sdk_session_id text NOT NULL,
    project text NOT NULL,
    scope text DEFAULT 'project'::text,
    agent_id text DEFAULT ''::text,
    agent_source text DEFAULT 'unknown'::text,
    type text NOT NULL,
    memory_type text,
    source_type text,
    created_at text NOT NULL,
    facts text,
    rejected jsonb DEFAULT '[]'::jsonb,
    narrative text,
    concepts jsonb,
    files_read jsonb,
    files_modified jsonb,
    commands_run jsonb,
    subtitle text,
    title text,
    archived_reason text,
    score_updated_at_epoch bigint,
    prompt_number bigint,
    archived_at_epoch bigint,
    last_retrieved_at_epoch bigint,
    id bigint NOT NULL,
    importance_score real DEFAULT 1,
    utility_score real DEFAULT 0.5,
    user_feedback bigint DEFAULT 0 NOT NULL,
    is_suppressed boolean DEFAULT false NOT NULL,
    retrieval_count bigint DEFAULT 0,
    injection_count bigint DEFAULT 0,
    created_at_epoch bigint NOT NULL,
    discovery_tokens bigint DEFAULT 0,
    is_superseded bigint DEFAULT 0,
    is_archived bigint DEFAULT 0,
    encrypted_secret bytea,
    encryption_key_fingerprint text,
    expires_at timestamp with time zone,
    ttl_days integer,
    status text DEFAULT 'active'::text,
    status_reason text,
    effectiveness_score real DEFAULT 0,
    effectiveness_injections bigint DEFAULT 0,
    effectiveness_successes bigint DEFAULT 0,
    enrichment_level integer DEFAULT 0 NOT NULL,
    source_event_ids bigint[],
    raw_content text,
    search_vector tsvector GENERATED ALWAYS AS ((to_tsvector('english'::regconfig, ((((COALESCE(title, ''::text) || ' '::text) || COALESCE(subtitle, ''::text)) || ' '::text) || COALESCE(narrative, ''::text))) || to_tsvector('simple'::regconfig, ((((COALESCE(title, ''::text) || ' '::text) || COALESCE(subtitle, ''::text)) || ' '::text) || COALESCE(narrative, ''::text))))) STORED,
    CONSTRAINT chk_observations_agent_source CHECK ((agent_source = ANY (ARRAY['claude-code'::text, 'codex'::text, 'gemini'::text, 'other'::text, 'unknown'::text]))),
    CONSTRAINT chk_observations_scope CHECK ((scope = ANY (ARRAY['project'::text, 'global'::text, 'agent'::text]))),
    CONSTRAINT chk_observations_type CHECK ((type = ANY (ARRAY['decision'::text, 'bugfix'::text, 'feature'::text, 'refactor'::text, 'discovery'::text, 'change'::text, 'guidance'::text, 'credential'::text, 'entity'::text, 'wiki'::text, 'pitfall'::text, 'operational'::text, 'timeline'::text])))
);


--
-- Name: observations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.observations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: observations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.observations_id_seq OWNED BY public.observations.id;


--
-- Name: patterns; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.patterns (
    status text DEFAULT 'active'::text,
    name text NOT NULL,
    type text NOT NULL,
    created_at text NOT NULL,
    last_seen_at text NOT NULL,
    signature text,
    projects text,
    observation_ids text,
    recommendation text,
    description text,
    merged_into_id bigint,
    frequency bigint DEFAULT 1,
    confidence real DEFAULT 0.5,
    id bigint NOT NULL,
    last_seen_at_epoch bigint NOT NULL,
    created_at_epoch bigint NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, ((((COALESCE(name, ''::text) || ' '::text) || COALESCE(description, ''::text)) || ' '::text) || COALESCE(recommendation, ''::text)))) STORED,
    CONSTRAINT chk_patterns_status CHECK ((status = ANY (ARRAY['active'::text, 'deprecated'::text, 'merged'::text]))),
    CONSTRAINT chk_patterns_type CHECK ((type = ANY (ARRAY['bug'::text, 'refactor'::text, 'architecture'::text, 'anti-pattern'::text, 'best-practice'::text])))
);


--
-- Name: patterns_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.patterns_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: patterns_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.patterns_id_seq OWNED BY public.patterns.id;


--
-- Name: project_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_settings (
    project text NOT NULL,
    relevance_threshold double precision DEFAULT 0.3 NOT NULL,
    feedback_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.projects (
    id text NOT NULL,
    git_remote text,
    relative_path text,
    legacy_ids text[],
    display_name text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    removed_at timestamp with time zone,
    last_heartbeat timestamp with time zone DEFAULT now()
);


--
-- Name: raw_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.raw_events (
    id bigint NOT NULL,
    session_id text NOT NULL,
    tool_name text NOT NULL,
    tool_input jsonb,
    tool_result jsonb,
    created_at_epoch bigint DEFAULT ((EXTRACT(epoch FROM now()) * (1000)::numeric))::bigint NOT NULL,
    project text DEFAULT ''::text NOT NULL,
    workstation_id text DEFAULT ''::text NOT NULL,
    processed boolean DEFAULT false NOT NULL
);


--
-- Name: raw_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.raw_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: raw_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.raw_events_id_seq OWNED BY public.raw_events.id;


--
-- Name: reasoning_traces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.reasoning_traces (
    id bigint NOT NULL,
    sdk_session_id text NOT NULL,
    project text DEFAULT ''::text NOT NULL,
    steps jsonb DEFAULT '[]'::jsonb NOT NULL,
    quality_score real DEFAULT 0 NOT NULL,
    task_context jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    created_at_epoch bigint DEFAULT 0 NOT NULL
);


--
-- Name: reasoning_traces_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.reasoning_traces_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: reasoning_traces_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.reasoning_traces_id_seq OWNED BY public.reasoning_traces.id;


--
-- Name: retrieval_stats_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.retrieval_stats_log (
    id bigint NOT NULL,
    project text NOT NULL,
    event_type text NOT NULL,
    count integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: retrieval_stats_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.retrieval_stats_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: retrieval_stats_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.retrieval_stats_log_id_seq OWNED BY public.retrieval_stats_log.id;


--
-- Name: sdk_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sdk_sessions (
    claude_session_id text NOT NULL,
    project text NOT NULL,
    status text DEFAULT 'active'::text,
    started_at text NOT NULL,
    sdk_session_id text,
    user_prompt text,
    completed_at text,
    worker_port bigint,
    completed_at_epoch bigint,
    outcome text,
    outcome_reason text,
    outcome_recorded_at timestamp with time zone,
    utility_propagated_at timestamp with time zone,
    injection_strategy text,
    id bigint NOT NULL,
    prompt_counter bigint DEFAULT 0,
    started_at_epoch bigint NOT NULL,
    CONSTRAINT chk_sdk_sessions_status CHECK ((status = ANY (ARRAY['active'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: sdk_sessions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.sdk_sessions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: sdk_sessions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.sdk_sessions_id_seq OWNED BY public.sdk_sessions.id;


--
-- Name: search_misses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_misses (
    id bigint NOT NULL,
    project text NOT NULL,
    query text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: search_misses_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.search_misses_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: search_misses_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.search_misses_id_seq OWNED BY public.search_misses.id;


--
-- Name: search_query_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_query_log (
    id bigint NOT NULL,
    project text,
    query text NOT NULL,
    search_type text NOT NULL,
    results integer DEFAULT 0 NOT NULL,
    used_vector boolean DEFAULT false NOT NULL,
    latency_ms real,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: search_query_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.search_query_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: search_query_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.search_query_log_id_seq OWNED BY public.search_query_log.id;


--
-- Name: session_observation_injections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_observation_injections (
    id bigint NOT NULL,
    session_id bigint NOT NULL,
    observation_id bigint NOT NULL,
    injected_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: session_observation_injections_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.session_observation_injections_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: session_observation_injections_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.session_observation_injections_id_seq OWNED BY public.session_observation_injections.id;


--
-- Name: session_summaries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_summaries (
    created_at text NOT NULL,
    sdk_session_id text NOT NULL,
    project text NOT NULL,
    completed text,
    investigated text,
    learned text,
    next_steps text,
    notes text,
    request text,
    prompt_number bigint,
    id bigint NOT NULL,
    discovery_tokens bigint DEFAULT 0,
    created_at_epoch bigint NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, ((((((((((COALESCE(request, ''::text) || ' '::text) || COALESCE(investigated, ''::text)) || ' '::text) || COALESCE(learned, ''::text)) || ' '::text) || COALESCE(completed, ''::text)) || ' '::text) || COALESCE(next_steps, ''::text)) || ' '::text) || COALESCE(notes, ''::text)))) STORED
);


--
-- Name: session_summaries_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.session_summaries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: session_summaries_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.session_summaries_id_seq OWNED BY public.session_summaries.id;


--
-- Name: sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sessions (
    id character varying(64) NOT NULL,
    user_id integer NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    expires_at timestamp without time zone NOT NULL
);


--
-- Name: system_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_config (
    key text NOT NULL,
    value text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: telemetry_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.telemetry_snapshots (
    id bigint NOT NULL,
    snapshot_type text NOT NULL,
    project text DEFAULT ''::text NOT NULL,
    data jsonb NOT NULL,
    created_at_epoch bigint NOT NULL
);


--
-- Name: telemetry_snapshots_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.telemetry_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: telemetry_snapshots_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.telemetry_snapshots_id_seq OWNED BY public.telemetry_snapshots.id;


--
-- Name: user_prompts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_prompts (
    claude_session_id text NOT NULL,
    prompt_text text NOT NULL,
    created_at text NOT NULL,
    id bigint NOT NULL,
    prompt_number bigint NOT NULL,
    matched_observations bigint DEFAULT 0,
    created_at_epoch bigint NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, COALESCE(prompt_text, ''::text))) STORED
);


--
-- Name: user_prompts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.user_prompts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: user_prompts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.user_prompts_id_seq OWNED BY public.user_prompts.id;


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    email character varying(255) NOT NULL,
    password_hash character varying(255) DEFAULT ''::character varying NOT NULL,
    role character varying(20) DEFAULT 'operator'::character varying NOT NULL,
    disabled boolean DEFAULT false NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    last_login_at timestamp without time zone
);


--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;


--
-- Name: vectors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.vectors (
    doc_id text NOT NULL,
    embedding public.vector(1536) NOT NULL,
    sqlite_id bigint,
    doc_type text,
    field_type text,
    project text,
    scope text,
    model_version text
);


--
-- Name: versioned_document_comments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.versioned_document_comments (
    id bigint NOT NULL,
    document_id bigint NOT NULL,
    author text NOT NULL,
    content text NOT NULL,
    line_start integer,
    line_end integer,
    status text DEFAULT 'open'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: versioned_document_comments_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.versioned_document_comments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: versioned_document_comments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.versioned_document_comments_id_seq OWNED BY public.versioned_document_comments.id;


--
-- Name: versioned_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.versioned_documents (
    id bigint NOT NULL,
    path text NOT NULL,
    project text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    content text NOT NULL,
    content_hash text NOT NULL,
    doc_type text DEFAULT 'markdown'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    author text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: versioned_documents_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.versioned_documents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: versioned_documents_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.versioned_documents_id_seq OWNED BY public.versioned_documents.id;


--
-- Name: documents id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents ALTER COLUMN id SET DEFAULT nextval('public.documents_id_seq'::regclass);


--
-- Name: injection_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.injection_log ALTER COLUMN id SET DEFAULT nextval('public.injection_log_id_seq'::regclass);


--
-- Name: invitations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations ALTER COLUMN id SET DEFAULT nextval('public.invitations_id_seq'::regclass);


--
-- Name: issue_comments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.issue_comments ALTER COLUMN id SET DEFAULT nextval('public.issue_comments_id_seq'::regclass);


--
-- Name: issues id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.issues ALTER COLUMN id SET DEFAULT nextval('public.issues_id_seq'::regclass);


--
-- Name: observation_conflicts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_conflicts ALTER COLUMN id SET DEFAULT nextval('public.observation_conflicts_id_seq'::regclass);


--
-- Name: observation_injections id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_injections ALTER COLUMN id SET DEFAULT nextval('public.observation_injections_id_seq'::regclass);


--
-- Name: observation_relations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_relations ALTER COLUMN id SET DEFAULT nextval('public.observation_relations_id_seq'::regclass);


--
-- Name: observation_versions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_versions ALTER COLUMN id SET DEFAULT nextval('public.observation_versions_id_seq'::regclass);


--
-- Name: observations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observations ALTER COLUMN id SET DEFAULT nextval('public.observations_id_seq'::regclass);


--
-- Name: patterns id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.patterns ALTER COLUMN id SET DEFAULT nextval('public.patterns_id_seq'::regclass);


--
-- Name: raw_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_events ALTER COLUMN id SET DEFAULT nextval('public.raw_events_id_seq'::regclass);


--
-- Name: reasoning_traces id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reasoning_traces ALTER COLUMN id SET DEFAULT nextval('public.reasoning_traces_id_seq'::regclass);


--
-- Name: retrieval_stats_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.retrieval_stats_log ALTER COLUMN id SET DEFAULT nextval('public.retrieval_stats_log_id_seq'::regclass);


--
-- Name: sdk_sessions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sdk_sessions ALTER COLUMN id SET DEFAULT nextval('public.sdk_sessions_id_seq'::regclass);


--
-- Name: search_misses id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_misses ALTER COLUMN id SET DEFAULT nextval('public.search_misses_id_seq'::regclass);


--
-- Name: search_query_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_query_log ALTER COLUMN id SET DEFAULT nextval('public.search_query_log_id_seq'::regclass);


--
-- Name: session_observation_injections id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_observation_injections ALTER COLUMN id SET DEFAULT nextval('public.session_observation_injections_id_seq'::regclass);


--
-- Name: session_summaries id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_summaries ALTER COLUMN id SET DEFAULT nextval('public.session_summaries_id_seq'::regclass);


--
-- Name: telemetry_snapshots id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telemetry_snapshots ALTER COLUMN id SET DEFAULT nextval('public.telemetry_snapshots_id_seq'::regclass);


--
-- Name: user_prompts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_prompts ALTER COLUMN id SET DEFAULT nextval('public.user_prompts_id_seq'::regclass);


--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);


--
-- Name: versioned_document_comments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.versioned_document_comments ALTER COLUMN id SET DEFAULT nextval('public.versioned_document_comments_id_seq'::regclass);


--
-- Name: versioned_documents id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.versioned_documents ALTER COLUMN id SET DEFAULT nextval('public.versioned_documents_id_seq'::regclass);


--
-- Data for Name: agent_observation_stats; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.agent_observation_stats (agent_id, observation_id, injections, successes, updated_at) FROM stdin;
\.


--
-- Data for Name: api_tokens; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.api_tokens (id, name, token_hash, token_prefix, scope, created_at, last_used_at, request_count, error_count, revoked, revoked_at) FROM stdin;
\.


--
-- Data for Name: concept_weights; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.concept_weights (concept, updated_at, weight) FROM stdin;
security	2026-07-13T00:31:26+03:00	0.3
gotcha	2026-07-13T00:31:26+03:00	0.25
best-practice	2026-07-13T00:31:26+03:00	0.2
anti-pattern	2026-07-13T00:31:26+03:00	0.2
architecture	2026-07-13T00:31:26+03:00	0.15
performance	2026-07-13T00:31:26+03:00	0.15
error-handling	2026-07-13T00:31:26+03:00	0.15
pattern	2026-07-13T00:31:26+03:00	0.1
testing	2026-07-13T00:31:26+03:00	0.1
debugging	2026-07-13T00:31:26+03:00	0.1
workflow	2026-07-13T00:31:26+03:00	0.05
tooling	2026-07-13T00:31:26+03:00	0.05
\.


--
-- Data for Name: content; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.content (hash, doc, created_at) FROM stdin;
\.


--
-- Data for Name: content_chunks; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.content_chunks (hash, seq, pos, model, embedding, created_at, text) FROM stdin;
\.


--
-- Data for Name: documents; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.documents (id, collection, path, title, hash, active, created_at, updated_at) FROM stdin;
\.


--
-- Data for Name: indexed_sessions; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.indexed_sessions (id, workstation_id, project_id, project_path, git_branch, first_msg_at, last_msg_at, exchange_count, tool_counts, topics, content, file_mtime, indexed_at) FROM stdin;
\.


--
-- Data for Name: injection_log; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.injection_log (id, observation_id, project, task_context, session_id, created_at, cited) FROM stdin;
\.


--
-- Data for Name: invitations; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.invitations (id, code, created_by, used_by, used_at, created_at) FROM stdin;
\.


--
-- Data for Name: issue_comments; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.issue_comments (id, issue_id, author_project, author_agent, body, created_at) FROM stdin;
\.


--
-- Data for Name: issues; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.issues (id, title, body, status, priority, source_project, target_project, source_agent, created_by_session, labels, acknowledged_at, resolved_at, reopened_at, created_at, updated_at, closed_at, type) FROM stdin;
\.


--
-- Data for Name: migrations; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.migrations (id) FROM stdin;
001_core_tables
002_user_prompts
003_user_prompts_fts
004_observations_fts
005_session_summaries_fts
006_sqlite_vec_vectors
007_concept_weights
008_observation_conflicts
009_patterns
010_patterns_fts
011_observation_relations
012_query_optimization_indexes
013_observation_archival
014_performance_indexes
015_optimized_composite_indexes
016_relation_and_active_indexes
017_content_addressable_storage
018_session_indexing
019_extended_relation_types
020_configurable_vector_dimensions
021_fix_patterns_indexes
022_raw_events
023_observation_enrichment
024_memory_blocks
025_utility_tracking
026_telemetry_snapshots
027_observation_source_type
028_session_observation_injections
029_content_chunks_text
030_projects_table
031_credential_storage
032_agent_scoping
033_create_search_misses
034_credential_uniqueness_and_search_miss_index
035_decision_rejected_field
036_api_tokens
037_search_query_log
038_retrieval_stats_log
039_observations_verified_ttl
040_cleanup_garbage_observations
041_purge_orphan_vectors
042_purge_low_quality_patterns
043_radical_observation_cleanup
044_observation_user_feedback
045_observation_is_suppressed
046_injection_log
047_drop_memory_blocks
048_gin_indexes_concepts_files
049_project_settings
050_system_config
051_documents
052_cleanup_phantom_bulk_import_sessions
053_cleanup_dead_vault_credentials
054_observation_status_lifecycle
055_backfill_null_status
056_backfill_memory_type
057_session_outcome_columns
058_observation_injections_table
059_observation_effectiveness_columns
060_agent_observation_stats
061_observation_versions
062_cleanup_phantom_bulk_import_sessions
063_backfill_observation_concepts
064_backfill_missing_concepts
065_reasoning_traces
066_injection_log_cited_column
067_relation_temporal_validity
068_expand_observation_type_check
069_gstack_insights
070_agent_issues
071_issues_lifecycle_v2
072_sessions_utility_propagated_at
073_sessions_utility_propagated_at_index
074_observations_commands_run
075_issues_type
076_observations_fts_multilang
077_relations_constraints_update
078_merge_duplicate_project_slugs
079_merge_duplicate_projects_followup
080_create_auth_tables
081_project_identity_pure_hash
082_projects_lifecycle
\.


--
-- Data for Name: observation_conflicts; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.observation_conflicts (conflict_type, resolution, detected_at, reason, resolved_at, id, newer_obs_id, older_obs_id, detected_at_epoch, resolved) FROM stdin;
\.


--
-- Data for Name: observation_injections; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.observation_injections (id, observation_id, session_id, injection_section, injected_at) FROM stdin;
\.


--
-- Data for Name: observation_relations; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.observation_relations (relation_type, detection_source, created_at, reason, id, source_id, target_id, confidence, created_at_epoch, valid_from, valid_to) FROM stdin;
\.


--
-- Data for Name: observation_versions; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.observation_versions (id, observation_id, version, narrative, is_active, created_at, source) FROM stdin;
\.


--
-- Data for Name: observations; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.observations (file_mtimes, sdk_session_id, project, scope, agent_id, agent_source, type, memory_type, source_type, created_at, facts, rejected, narrative, concepts, files_read, files_modified, commands_run, subtitle, title, archived_reason, score_updated_at_epoch, prompt_number, archived_at_epoch, last_retrieved_at_epoch, id, importance_score, utility_score, user_feedback, is_suppressed, retrieval_count, injection_count, created_at_epoch, discovery_tokens, is_superseded, is_archived, encrypted_secret, encryption_key_fingerprint, expires_at, ttl_days, status, status_reason, effectiveness_score, effectiveness_injections, effectiveness_successes, enrichment_level, source_event_ids, raw_content) FROM stdin;
\.


--
-- Data for Name: patterns; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.patterns (status, name, type, created_at, last_seen_at, signature, projects, observation_ids, recommendation, description, merged_into_id, frequency, confidence, id, last_seen_at_epoch, created_at_epoch) FROM stdin;
\.


--
-- Data for Name: project_settings; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.project_settings (project, relevance_threshold, feedback_count, updated_at) FROM stdin;
\.


--
-- Data for Name: projects; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.projects (id, git_remote, relative_path, legacy_ids, display_name, created_at, removed_at, last_heartbeat) FROM stdin;
\.


--
-- Data for Name: raw_events; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.raw_events (id, session_id, tool_name, tool_input, tool_result, created_at_epoch, project, workstation_id, processed) FROM stdin;
\.


--
-- Data for Name: reasoning_traces; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.reasoning_traces (id, sdk_session_id, project, steps, quality_score, task_context, created_at, created_at_epoch) FROM stdin;
\.


--
-- Data for Name: retrieval_stats_log; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.retrieval_stats_log (id, project, event_type, count, created_at) FROM stdin;
\.


--
-- Data for Name: sdk_sessions; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.sdk_sessions (claude_session_id, project, status, started_at, sdk_session_id, user_prompt, completed_at, worker_port, completed_at_epoch, outcome, outcome_reason, outcome_recorded_at, utility_propagated_at, injection_strategy, id, prompt_counter, started_at_epoch) FROM stdin;
\.


--
-- Data for Name: search_misses; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.search_misses (id, project, query, created_at) FROM stdin;
\.


--
-- Data for Name: search_query_log; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.search_query_log (id, project, query, search_type, results, used_vector, latency_ms, created_at) FROM stdin;
\.


--
-- Data for Name: session_observation_injections; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.session_observation_injections (id, session_id, observation_id, injected_at) FROM stdin;
\.


--
-- Data for Name: session_summaries; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.session_summaries (created_at, sdk_session_id, project, completed, investigated, learned, next_steps, notes, request, prompt_number, id, discovery_tokens, created_at_epoch) FROM stdin;
\.


--
-- Data for Name: sessions; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.sessions (id, user_id, created_at, expires_at) FROM stdin;
\.


--
-- Data for Name: system_config; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.system_config (key, value, updated_at) FROM stdin;
\.


--
-- Data for Name: telemetry_snapshots; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.telemetry_snapshots (id, snapshot_type, project, data, created_at_epoch) FROM stdin;
\.


--
-- Data for Name: user_prompts; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.user_prompts (claude_session_id, prompt_text, created_at, id, prompt_number, matched_observations, created_at_epoch) FROM stdin;
\.


--
-- Data for Name: users; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.users (id, email, password_hash, role, disabled, created_at, last_login_at) FROM stdin;
\.


--
-- Data for Name: vectors; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.vectors (doc_id, embedding, sqlite_id, doc_type, field_type, project, scope, model_version) FROM stdin;
\.


--
-- Data for Name: versioned_document_comments; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.versioned_document_comments (id, document_id, author, content, line_start, line_end, status, created_at) FROM stdin;
\.


--
-- Data for Name: versioned_documents; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.versioned_documents (id, path, project, version, content, content_hash, doc_type, metadata, author, created_at) FROM stdin;
\.


--
-- Name: documents_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.documents_id_seq', 1, false);


--
-- Name: injection_log_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.injection_log_id_seq', 1, false);


--
-- Name: invitations_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.invitations_id_seq', 1, false);


--
-- Name: issue_comments_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.issue_comments_id_seq', 1, false);


--
-- Name: issues_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.issues_id_seq', 1, false);


--
-- Name: observation_conflicts_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.observation_conflicts_id_seq', 1, false);


--
-- Name: observation_injections_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.observation_injections_id_seq', 1, false);


--
-- Name: observation_relations_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.observation_relations_id_seq', 1, false);


--
-- Name: observation_versions_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.observation_versions_id_seq', 1, false);


--
-- Name: observations_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.observations_id_seq', 1, false);


--
-- Name: patterns_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.patterns_id_seq', 1, false);


--
-- Name: raw_events_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.raw_events_id_seq', 1, false);


--
-- Name: reasoning_traces_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.reasoning_traces_id_seq', 1, false);


--
-- Name: retrieval_stats_log_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.retrieval_stats_log_id_seq', 1, false);


--
-- Name: sdk_sessions_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.sdk_sessions_id_seq', 1, false);


--
-- Name: search_misses_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.search_misses_id_seq', 1, false);


--
-- Name: search_query_log_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.search_query_log_id_seq', 1, false);


--
-- Name: session_observation_injections_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.session_observation_injections_id_seq', 1, false);


--
-- Name: session_summaries_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.session_summaries_id_seq', 1, false);


--
-- Name: telemetry_snapshots_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.telemetry_snapshots_id_seq', 1, false);


--
-- Name: user_prompts_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.user_prompts_id_seq', 1, false);


--
-- Name: users_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.users_id_seq', 1, false);


--
-- Name: versioned_document_comments_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.versioned_document_comments_id_seq', 1, false);


--
-- Name: versioned_documents_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.versioned_documents_id_seq', 1, false);


--
-- Name: agent_observation_stats agent_observation_stats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_observation_stats
    ADD CONSTRAINT agent_observation_stats_pkey PRIMARY KEY (agent_id, observation_id);


--
-- Name: api_tokens api_tokens_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_tokens
    ADD CONSTRAINT api_tokens_name_key UNIQUE (name);


--
-- Name: api_tokens api_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_tokens
    ADD CONSTRAINT api_tokens_pkey PRIMARY KEY (id);


--
-- Name: concept_weights concept_weights_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.concept_weights
    ADD CONSTRAINT concept_weights_pkey PRIMARY KEY (concept);


--
-- Name: content_chunks content_chunks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.content_chunks
    ADD CONSTRAINT content_chunks_pkey PRIMARY KEY (hash, seq);


--
-- Name: content content_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.content
    ADD CONSTRAINT content_pkey PRIMARY KEY (hash);


--
-- Name: documents documents_collection_path_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_collection_path_key UNIQUE (collection, path);


--
-- Name: documents documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_pkey PRIMARY KEY (id);


--
-- Name: indexed_sessions indexed_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.indexed_sessions
    ADD CONSTRAINT indexed_sessions_pkey PRIMARY KEY (id);


--
-- Name: injection_log injection_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.injection_log
    ADD CONSTRAINT injection_log_pkey PRIMARY KEY (id);


--
-- Name: invitations invitations_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_code_key UNIQUE (code);


--
-- Name: invitations invitations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_pkey PRIMARY KEY (id);


--
-- Name: issue_comments issue_comments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.issue_comments
    ADD CONSTRAINT issue_comments_pkey PRIMARY KEY (id);


--
-- Name: issues issues_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.issues
    ADD CONSTRAINT issues_pkey PRIMARY KEY (id);


--
-- Name: migrations migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.migrations
    ADD CONSTRAINT migrations_pkey PRIMARY KEY (id);


--
-- Name: observation_conflicts observation_conflicts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_conflicts
    ADD CONSTRAINT observation_conflicts_pkey PRIMARY KEY (id);


--
-- Name: observation_injections observation_injections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_injections
    ADD CONSTRAINT observation_injections_pkey PRIMARY KEY (id);


--
-- Name: observation_relations observation_relations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_relations
    ADD CONSTRAINT observation_relations_pkey PRIMARY KEY (id);


--
-- Name: observation_versions observation_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observation_versions
    ADD CONSTRAINT observation_versions_pkey PRIMARY KEY (id);


--
-- Name: observations observations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.observations
    ADD CONSTRAINT observations_pkey PRIMARY KEY (id);


--
-- Name: patterns patterns_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.patterns
    ADD CONSTRAINT patterns_pkey PRIMARY KEY (id);


--
-- Name: project_settings project_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_settings
    ADD CONSTRAINT project_settings_pkey PRIMARY KEY (project);


--
-- Name: projects projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);


--
-- Name: raw_events raw_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_events
    ADD CONSTRAINT raw_events_pkey PRIMARY KEY (id);


--
-- Name: reasoning_traces reasoning_traces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reasoning_traces
    ADD CONSTRAINT reasoning_traces_pkey PRIMARY KEY (id);


--
-- Name: retrieval_stats_log retrieval_stats_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.retrieval_stats_log
    ADD CONSTRAINT retrieval_stats_log_pkey PRIMARY KEY (id);


--
-- Name: sdk_sessions sdk_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sdk_sessions
    ADD CONSTRAINT sdk_sessions_pkey PRIMARY KEY (id);


--
-- Name: search_misses search_misses_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_misses
    ADD CONSTRAINT search_misses_pkey PRIMARY KEY (id);


--
-- Name: search_query_log search_query_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_query_log
    ADD CONSTRAINT search_query_log_pkey PRIMARY KEY (id);


--
-- Name: session_observation_injections session_observation_injections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_observation_injections
    ADD CONSTRAINT session_observation_injections_pkey PRIMARY KEY (id);


--
-- Name: session_observation_injections session_observation_injections_session_id_observation_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_observation_injections
    ADD CONSTRAINT session_observation_injections_session_id_observation_id_key UNIQUE (session_id, observation_id);


--
-- Name: session_summaries session_summaries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_summaries
    ADD CONSTRAINT session_summaries_pkey PRIMARY KEY (id);


--
-- Name: sessions sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);


--
-- Name: system_config system_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_config
    ADD CONSTRAINT system_config_pkey PRIMARY KEY (key);


--
-- Name: telemetry_snapshots telemetry_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telemetry_snapshots
    ADD CONSTRAINT telemetry_snapshots_pkey PRIMARY KEY (id);


--
-- Name: user_prompts user_prompts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_prompts
    ADD CONSTRAINT user_prompts_pkey PRIMARY KEY (id);


--
-- Name: users users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: vectors vectors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vectors
    ADD CONSTRAINT vectors_pkey PRIMARY KEY (doc_id);


--
-- Name: versioned_document_comments versioned_document_comments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.versioned_document_comments
    ADD CONSTRAINT versioned_document_comments_pkey PRIMARY KEY (id);


--
-- Name: versioned_documents versioned_documents_path_project_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.versioned_documents
    ADD CONSTRAINT versioned_documents_path_project_version_key UNIQUE (path, project, version);


--
-- Name: versioned_documents versioned_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.versioned_documents
    ADD CONSTRAINT versioned_documents_pkey PRIMARY KEY (id);


--
-- Name: idx_api_tokens_prefix; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_tokens_prefix ON public.api_tokens USING btree (token_prefix) WHERE (NOT revoked);


--
-- Name: idx_conflicts_newer; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conflicts_newer ON public.observation_conflicts USING btree (newer_obs_id);


--
-- Name: idx_conflicts_older; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conflicts_older ON public.observation_conflicts USING btree (older_obs_id);


--
-- Name: idx_conflicts_unresolved; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conflicts_unresolved ON public.observation_conflicts USING btree (resolved, detected_at_epoch DESC);


--
-- Name: idx_content_chunks_embedding_hnsw; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_content_chunks_embedding_hnsw ON public.content_chunks USING hnsw (embedding public.vector_cosine_ops) WITH (m='16', ef_construction='64');


--
-- Name: idx_content_chunks_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_content_chunks_hash ON public.content_chunks USING btree (hash);


--
-- Name: idx_documents_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_documents_active ON public.documents USING btree (active) WHERE (active = true);


--
-- Name: idx_documents_collection; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_documents_collection ON public.documents USING btree (collection);


--
-- Name: idx_documents_fts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_documents_fts ON public.documents USING gin (search_vector);


--
-- Name: idx_documents_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_documents_hash ON public.documents USING btree (hash);


--
-- Name: idx_injection_log_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_injection_log_created_at ON public.injection_log USING btree (created_at);


--
-- Name: idx_injection_log_observation_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_injection_log_observation_id ON public.injection_log USING btree (observation_id);


--
-- Name: idx_injection_log_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_injection_log_project ON public.injection_log USING btree (project);


--
-- Name: idx_injection_log_session_cited; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_injection_log_session_cited ON public.injection_log USING btree (session_id, cited);


--
-- Name: idx_issue_comments_issue_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_issue_comments_issue_created ON public.issue_comments USING btree (issue_id, created_at);


--
-- Name: idx_issues_source_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_issues_source_project ON public.issues USING btree (source_project);


--
-- Name: idx_issues_target_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_issues_target_status ON public.issues USING btree (target_project, status);


--
-- Name: idx_obs_injections_obs; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_obs_injections_obs ON public.observation_injections USING btree (observation_id);


--
-- Name: idx_obs_injections_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_obs_injections_session ON public.observation_injections USING btree (session_id);


--
-- Name: idx_obs_versions_obs; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_obs_versions_obs ON public.observation_versions USING btree (observation_id);


--
-- Name: idx_observations_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_active ON public.observations USING btree (is_archived, is_superseded);


--
-- Name: idx_observations_agent_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_agent_id ON public.observations USING btree (agent_id);


--
-- Name: idx_observations_agent_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_agent_source ON public.observations USING btree (agent_source);


--
-- Name: idx_observations_archived; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_archived ON public.observations USING btree (is_archived);


--
-- Name: idx_observations_concepts_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_concepts_gin ON public.observations USING gin (concepts);


--
-- Name: idx_observations_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_created ON public.observations USING btree (created_at_epoch DESC);


--
-- Name: idx_observations_credential_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_observations_credential_unique ON public.observations USING btree (project, title) WHERE (type = 'credential'::text);


--
-- Name: idx_observations_enrichment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_enrichment ON public.observations USING btree (enrichment_level, created_at_epoch DESC);


--
-- Name: idx_observations_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_expires ON public.observations USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_observations_files_modified_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_files_modified_gin ON public.observations USING gin (files_modified);


--
-- Name: idx_observations_files_read_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_files_read_gin ON public.observations USING gin (files_read);


--
-- Name: idx_observations_fts_ordering; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_fts_ordering ON public.observations USING btree (project, importance_score DESC) WHERE (((is_archived = 0) OR (is_archived IS NULL)) AND ((is_superseded = 0) OR (is_superseded IS NULL)));


--
-- Name: idx_observations_global_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_global_scope ON public.observations USING btree (scope, importance_score DESC, created_at_epoch DESC) WHERE (scope = 'global'::text);


--
-- Name: idx_observations_id_covering; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_id_covering ON public.observations USING btree (id, project, scope, importance_score);


--
-- Name: idx_observations_importance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_importance ON public.observations USING btree (importance_score DESC);


--
-- Name: idx_observations_memory_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_memory_type ON public.observations USING btree (memory_type);


--
-- Name: idx_observations_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_project ON public.observations USING btree (project);


--
-- Name: idx_observations_project_covering; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_project_covering ON public.observations USING btree (project, scope, is_superseded, importance_score DESC) WHERE ((is_superseded = 0) OR (is_superseded IS NULL));


--
-- Name: idx_observations_project_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_project_created ON public.observations USING btree (project, created_at_epoch DESC);


--
-- Name: idx_observations_project_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_project_scope ON public.observations USING btree (scope);


--
-- Name: idx_observations_project_scope_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_project_scope_created ON public.observations USING btree (project, scope, created_at_epoch DESC, importance_score DESC);


--
-- Name: idx_observations_project_scope_importance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_project_scope_importance ON public.observations USING btree (project, scope, importance_score DESC, created_at_epoch DESC);


--
-- Name: idx_observations_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_scope ON public.observations USING btree (scope);


--
-- Name: idx_observations_score_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_score_updated ON public.observations USING btree (score_updated_at_epoch);


--
-- Name: idx_observations_sdk_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_sdk_session_id ON public.observations USING btree (sdk_session_id);


--
-- Name: idx_observations_search_vector; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_search_vector ON public.observations USING gin (search_vector);


--
-- Name: idx_observations_session_prompt; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_session_prompt ON public.observations USING btree (sdk_session_id, prompt_number DESC) WHERE (COALESCE(is_superseded, (0)::bigint) = 0);


--
-- Name: idx_observations_source_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_source_type ON public.observations USING btree (source_type);


--
-- Name: idx_observations_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_status ON public.observations USING btree (status);


--
-- Name: idx_observations_superseded; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_superseded ON public.observations USING btree (is_superseded);


--
-- Name: idx_observations_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_observations_type ON public.observations USING btree (type);


--
-- Name: idx_patterns_confidence; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_confidence ON public.patterns USING btree (confidence DESC);


--
-- Name: idx_patterns_frequency; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_frequency ON public.patterns USING btree (frequency DESC, last_seen_at_epoch DESC) WHERE (status = 'active'::text);


--
-- Name: idx_patterns_fts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_fts ON public.patterns USING gin (search_vector);


--
-- Name: idx_patterns_last_seen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_last_seen ON public.patterns USING btree (last_seen_at_epoch DESC);


--
-- Name: idx_patterns_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_status ON public.patterns USING btree (status);


--
-- Name: idx_patterns_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_type ON public.patterns USING btree (type);


--
-- Name: idx_patterns_type_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_patterns_type_project ON public.patterns USING btree (type, frequency DESC) WHERE (status = 'active'::text);


--
-- Name: idx_projects_last_heartbeat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_last_heartbeat ON public.projects USING btree (last_heartbeat);


--
-- Name: idx_projects_legacy_ids; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_legacy_ids ON public.projects USING gin (legacy_ids);


--
-- Name: idx_projects_remote_path; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_projects_remote_path ON public.projects USING btree (git_remote, relative_path) WHERE (git_remote IS NOT NULL);


--
-- Name: idx_projects_removed_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_removed_at ON public.projects USING btree (removed_at) WHERE (removed_at IS NOT NULL);


--
-- Name: idx_prompts_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_prompts_created ON public.user_prompts USING btree (created_at_epoch DESC);


--
-- Name: idx_prompts_session_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_prompts_session_created ON public.user_prompts USING btree (claude_session_id, created_at_epoch DESC);


--
-- Name: idx_prompts_session_number; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_prompts_session_number ON public.user_prompts USING btree (claude_session_id, prompt_number);


--
-- Name: idx_raw_events_session_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_events_session_time ON public.raw_events USING btree (session_id, created_at_epoch);


--
-- Name: idx_raw_events_unprocessed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_events_unprocessed ON public.raw_events USING btree (created_at_epoch) WHERE (processed = false);


--
-- Name: idx_reasoning_traces_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_reasoning_traces_project ON public.reasoning_traces USING btree (project);


--
-- Name: idx_reasoning_traces_quality; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_reasoning_traces_quality ON public.reasoning_traces USING btree (quality_score);


--
-- Name: idx_reasoning_traces_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_reasoning_traces_session ON public.reasoning_traces USING btree (sdk_session_id);


--
-- Name: idx_relations_both; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relations_both ON public.observation_relations USING btree (source_id, target_id);


--
-- Name: idx_relations_confidence; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relations_confidence ON public.observation_relations USING btree (confidence DESC);


--
-- Name: idx_relations_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relations_source ON public.observation_relations USING btree (source_id);


--
-- Name: idx_relations_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relations_target ON public.observation_relations USING btree (target_id);


--
-- Name: idx_relations_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_relations_type ON public.observation_relations USING btree (relation_type);


--
-- Name: idx_relations_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_relations_unique ON public.observation_relations USING btree (source_id, target_id, relation_type);


--
-- Name: idx_retrieval_stats_project_type_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_retrieval_stats_project_type_created ON public.retrieval_stats_log USING btree (project, event_type, created_at DESC);


--
-- Name: idx_sdk_sessions_claude_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sdk_sessions_claude_session_id ON public.sdk_sessions USING btree (claude_session_id);


--
-- Name: idx_sdk_sessions_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sdk_sessions_project ON public.sdk_sessions USING btree (project);


--
-- Name: idx_sdk_sessions_sdk_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sdk_sessions_sdk_session_id ON public.sdk_sessions USING btree (sdk_session_id);


--
-- Name: idx_sdk_sessions_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sdk_sessions_status ON public.sdk_sessions USING btree (status);


--
-- Name: idx_sdk_sessions_utility_propagated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sdk_sessions_utility_propagated_at ON public.sdk_sessions USING btree (utility_propagated_at) WHERE (utility_propagated_at IS NOT NULL);


--
-- Name: idx_search_misses_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_search_misses_created ON public.search_misses USING btree (created_at);


--
-- Name: idx_search_misses_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_search_misses_project ON public.search_misses USING btree (project);


--
-- Name: idx_search_misses_project_query_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_search_misses_project_query_created ON public.search_misses USING btree (project, query, created_at DESC);


--
-- Name: idx_search_query_log_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_search_query_log_created ON public.search_query_log USING btree (created_at DESC);


--
-- Name: idx_search_query_log_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_search_query_log_project ON public.search_query_log USING btree (project, created_at DESC);


--
-- Name: idx_session_summaries_fts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_fts ON public.session_summaries USING gin (search_vector);


--
-- Name: idx_session_summaries_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_project ON public.session_summaries USING btree (project);


--
-- Name: idx_session_summaries_sdk_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_sdk_session_id ON public.session_summaries USING btree (sdk_session_id);


--
-- Name: idx_sessions_last_msg; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_last_msg ON public.indexed_sessions USING btree (last_msg_at DESC);


--
-- Name: idx_sessions_proj; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_proj ON public.indexed_sessions USING btree (project_id);


--
-- Name: idx_sessions_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_started ON public.sdk_sessions USING btree (started_at_epoch DESC);


--
-- Name: idx_sessions_tsv; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_tsv ON public.indexed_sessions USING gin (tsv);


--
-- Name: idx_sessions_ws; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_ws ON public.indexed_sessions USING btree (workstation_id);


--
-- Name: idx_sessions_ws_proj; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_ws_proj ON public.indexed_sessions USING btree (workstation_id, project_id);


--
-- Name: idx_soi_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_soi_session_id ON public.session_observation_injections USING btree (session_id);


--
-- Name: idx_summaries_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_summaries_created ON public.session_summaries USING btree (created_at_epoch DESC);


--
-- Name: idx_summaries_project_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_summaries_project_created ON public.session_summaries USING btree (project, created_at_epoch DESC);


--
-- Name: idx_telemetry_type_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_telemetry_type_time ON public.telemetry_snapshots USING btree (snapshot_type, created_at_epoch DESC);


--
-- Name: idx_user_prompts_claude_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_prompts_claude_session_id ON public.user_prompts USING btree (claude_session_id);


--
-- Name: idx_user_prompts_fts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_prompts_fts ON public.user_prompts USING gin (search_vector);


--
-- Name: idx_user_prompts_prompt_number; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_prompts_prompt_number ON public.user_prompts USING btree (prompt_number);


--
-- Name: idx_user_prompts_session_number_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_user_prompts_session_number_unique ON public.user_prompts USING btree (claude_session_id, prompt_number);


--
-- Name: idx_vectors_doc_type_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vectors_doc_type_project ON public.vectors USING btree (doc_type, project, scope);


--
-- Name: idx_vectors_embedding_hnsw; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vectors_embedding_hnsw ON public.vectors USING hnsw (embedding public.vector_cosine_ops) WITH (m='16', ef_construction='64');


--
-- Name: idx_vectors_observation_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vectors_observation_lookup ON public.vectors USING btree (doc_type, sqlite_id, project) WHERE (doc_type = 'observation'::text);


--
-- Name: idx_versioned_document_comments_doc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_versioned_document_comments_doc ON public.versioned_document_comments USING btree (document_id);


--
-- Name: idx_versioned_documents_doc_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_versioned_documents_doc_type ON public.versioned_documents USING btree (doc_type);


--
-- Name: idx_versioned_documents_project_path; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_versioned_documents_project_path ON public.versioned_documents USING btree (project, path, version DESC);


--
-- Name: content_chunks content_chunks_hash_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.content_chunks
    ADD CONSTRAINT content_chunks_hash_fkey FOREIGN KEY (hash) REFERENCES public.content(hash) ON DELETE CASCADE;


--
-- Name: documents documents_hash_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_hash_fkey FOREIGN KEY (hash) REFERENCES public.content(hash) ON DELETE SET NULL;


--
-- Name: injection_log injection_log_observation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.injection_log
    ADD CONSTRAINT injection_log_observation_id_fkey FOREIGN KEY (observation_id) REFERENCES public.observations(id) ON DELETE CASCADE;


--
-- Name: invitations invitations_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id);


--
-- Name: invitations invitations_used_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_used_by_fkey FOREIGN KEY (used_by) REFERENCES public.users(id);


--
-- Name: issue_comments issue_comments_issue_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.issue_comments
    ADD CONSTRAINT issue_comments_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issues(id) ON DELETE CASCADE;


--
-- Name: session_observation_injections session_observation_injections_observation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_observation_injections
    ADD CONSTRAINT session_observation_injections_observation_id_fkey FOREIGN KEY (observation_id) REFERENCES public.observations(id) ON DELETE CASCADE;


--
-- Name: session_observation_injections session_observation_injections_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_observation_injections
    ADD CONSTRAINT session_observation_injections_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.sdk_sessions(id) ON DELETE CASCADE;


--
-- Name: sessions sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: versioned_document_comments versioned_document_comments_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.versioned_document_comments
    ADD CONSTRAINT versioned_document_comments_document_id_fkey FOREIGN KEY (document_id) REFERENCES public.versioned_documents(id);


--
-- PostgreSQL database dump complete
--

\unrestrict GKgwz0mQo1LQTCewMYkbGpvBvO4ne6nkmZlHTgLn4c8wGNcYoO9uMIoXU0oQxmF
