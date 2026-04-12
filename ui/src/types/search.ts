import type { Observation } from './observation'

export interface SearchResultObservation extends Observation {
  similarity?: number
}

export interface ContextSearchResponse {
  project: string
  query: string
  intent: string
  expansions: Array<{
    query: string
    weight: number
    source: string
  }>
  observations: SearchResultObservation[]
  threshold: number
  max_results: number
}

export interface DecisionSearchResponse {
  project: string
  query: string
  observations: SearchResultObservation[]
  total_count: number
}
