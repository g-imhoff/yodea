import { useState } from "react"

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

  async function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    if (pending) return
    const value = email.trim()
    if (!DEV_NAME_RE.test(value) && !isEmailShape(value)) {
      setFailed(true)
      return
    }
    setPending(true)
    setFailed(false)
    try {
      await login(value, password)
      onLoggedIn(next)
    } catch {
      setFailed(true)
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
                <FieldLabel htmlFor="yodea-email">Email</FieldLabel>
                <Input
                  id="yodea-email"
                  type="text"
                  inputMode="email"
                  autoComplete="username"
                  required
                  value={email}
                  aria-invalid={failed}
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
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              {failed && (
                <Alert variant="destructive">
                  <AlertDescription>{FAILURE_TEXT}</AlertDescription>
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
