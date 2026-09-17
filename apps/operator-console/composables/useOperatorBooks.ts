export const BOOKS_RETIREMENT_MESSAGE = 'Plaintext book intake has been retired. Historical Documents and provenance remain available.'

export interface OperatorBooksRetirement {
  state: 'retired'
  message: string
  documentsHref: string
}

export function useOperatorBooks(): OperatorBooksRetirement {
  return {
    state: 'retired',
    message: BOOKS_RETIREMENT_MESSAGE,
    documentsHref: '/documents',
  }
}
