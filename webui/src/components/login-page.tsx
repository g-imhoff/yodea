import { useEffect, useRef, useState } from "react"

import { login } from "@/lib/api"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

const FAILURE_TEXT = "Login failed. Check email and password."
const SHAPE_TEXT = "Enter an email address or a dev username."
const ERROR_ID = "yodea-login-error"

const DEV_NAME_RE = /^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$/

function isEmailShape(value: string): boolean {
  const at = value.indexOf("@")
  return at > 0 && at < value.length - 1
}

export function LoginPage({
  next,
  onLoggedIn,
}: {
  next: string
  onLoggedIn: (next: string) => void
}) {
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [pending, setPending] = useState(false)
  const [failed, setFailed] = useState(false)
  const [errorText, setErrorText] = useState(FAILURE_TEXT)
  const emailRef = useRef<HTMLInputElement>(null)
  const alertRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let cancelled = false
    if (!cancelled) {
      emailRef.current?.focus()
    }
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (failed) {
      alertRef.current?.focus()
    }
  }, [failed, errorText])

  async function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    if (pending) return
    const value = email.trim()
    if (!DEV_NAME_RE.test(value) && !isEmailShape(value)) {
      setErrorText(SHAPE_TEXT)
      setFailed(true)
      alertRef.current?.focus()
      return
    }
    setPending(true)
    setFailed(false)
    try {
      await login(value, password)
      onLoggedIn(next)
    } catch {
      setErrorText(FAILURE_TEXT)
      setFailed(true)
      alertRef.current?.focus()
      setPending(false)
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>yodea</CardTitle>
          <CardDescription>Team login. Invited accounts only.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit}>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="yodea-email">Email or username</FieldLabel>
                <Input
                  id="yodea-email"
                  ref={emailRef}
                  type="text"
                  inputMode="email"
                  autoComplete="username"
                  required
                  value={email}
                  aria-invalid={failed}
                  aria-describedby={failed ? ERROR_ID : undefined}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="yodea-password">Password</FieldLabel>
                <Input
                  id="yodea-password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  aria-invalid={failed}
                  aria-describedby={failed ? ERROR_ID : undefined}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              {failed && (
                <Alert variant="destructive" ref={alertRef} tabIndex={-1}>
                  <AlertDescription id={ERROR_ID}>{errorText}</AlertDescription>
                </Alert>
              )}
              <Field>
                <Button type="submit" disabled={pending}>
                  {pending && <Spinner data-icon="inline-start" />}
                  {pending ? "Logging in" : "Log in"}
                </Button>
              </Field>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
