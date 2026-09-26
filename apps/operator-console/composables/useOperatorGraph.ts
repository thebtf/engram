export const GRAPH_RETIREMENT_MESSAGE = 'Manual knowledge-graph editing has been retired. Historical graph records remain readable through their existing consumers.'

export interface OperatorGraphRetirement {
  state: 'retired'
  message: string
  documentsHref: string
}

export function useOperatorGraph(): OperatorGraphRetirement {
  return {
    state: 'retired',
    message: GRAPH_RETIREMENT_MESSAGE,
    documentsHref: '/documents',
  }
}
