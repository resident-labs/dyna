// errorText is the message of a thrown value. A string, such as the error
// the API client throws, is its own message.
export function errorText(error: unknown): string {
  if (error == null) return String(error)
  const message: unknown = Reflect.get(Object(error), 'message')
  return String(message || error)
}
