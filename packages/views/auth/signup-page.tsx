"use client";

import { useState, useCallback, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
  CardFooter,
} from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { useAuthStore } from "@multica/core/auth";
import { useConfigStore } from "@multica/core/config";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { api } from "@multica/core/api";
import { paths } from "@multica/core/paths";
import { useT } from "../i18n";

interface SignupPageProps {
  logo?: ReactNode;
  onSuccess: () => void;
  onTokenObtained?: () => void;
}

const MIN_PASSWORD_LEN = 8;

export function SignupPage({
  logo,
  onSuccess,
  onTokenObtained,
}: SignupPageProps) {
  const { t } = useT("auth");
  const qc = useQueryClient();
  const passwordAuth = useConfigStore((state) => state.passwordAuth);
  const allowSignup = useConfigStore((state) => state.allowSignup);
  const signupTotpRequired = useConfigStore((state) => state.signupTotpRequired);
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const signupEnabled = passwordAuth && allowSignup;

  const handleSignup = useCallback(
    async (e?: React.FormEvent) => {
      e?.preventDefault();
      if (!username) {
        setError(t(($) => $.common.username_required));
        return;
      }
      if (!password) {
        setError(t(($) => $.common.password_required));
        return;
      }
      if (password.length < MIN_PASSWORD_LEN) {
        setError(t(($) => $.common.password_min));
        return;
      }
      if (password !== confirmPassword) {
        setError(t(($) => $.errors.password_mismatch));
        return;
      }
      if (signupTotpRequired && !totp.trim()) {
        setError(t(($) => $.common.totp_required));
        return;
      }
      setLoading(true);
      setError("");
      try {
        if (signupTotpRequired) {
          await useAuthStore.getState().signupWithPassword(
            username,
            password,
            email.trim(),
            totp.trim(),
          );
        } else {
          await useAuthStore.getState().signupWithPassword(
            username,
            password,
            email.trim(),
          );
        }
        const wsList = await api.listWorkspaces();
        qc.setQueryData(workspaceKeys.list(), wsList);
        onTokenObtained?.();
        onSuccess();
      } catch (err) {
        setError(
          err instanceof Error
            ? err.message
            : t(($) => $.errors.signup_failed),
        );
      } finally {
        setLoading(false);
      }
    },
    [username, email, password, confirmPassword, totp, signupTotpRequired, onSuccess, onTokenObtained, qc, t],
  );

  if (!signupEnabled) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center overflow-y-auto px-4 py-[max(1.5rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))] sm:flex-row">
        <Card className="my-auto w-full max-w-sm">
          <CardHeader className="text-center">
            {logo && <div className="mx-auto mb-4">{logo}</div>}
            <CardTitle className="text-display-sm">
              {t(($) => $.signup.title)}
            </CardTitle>
            <CardDescription>
              {t(($) => $.signup.disabled)}
            </CardDescription>
          </CardHeader>
          <CardFooter>
            <a
              href={paths.login()}
              className="w-full text-center text-body font-medium text-foreground underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground/70"
            >
              {t(($) => $.signup.sign_in)}
            </a>
          </CardFooter>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-svh flex-col items-center justify-center overflow-y-auto px-4 py-[max(1.5rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))] sm:flex-row">
      <Card className="my-auto w-full max-w-sm">
        <CardHeader className="text-center">
          {logo && <div className="mx-auto mb-4">{logo}</div>}
          <CardTitle className="text-display-sm">
            {t(($) => $.signup.title)}
          </CardTitle>
          <CardDescription>
            {t(($) => $.signup.description)}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <form id="signup-form" onSubmit={handleSignup} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="signup-username">{t(($) => $.common.username)}</Label>
              <Input
                id="signup-username"
                type="text"
                autoComplete="username"
                placeholder={t(($) => $.common.username_placeholder)}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoFocus
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-email">{t(($) => $.common.email_optional)}</Label>
              <Input
                id="signup-email"
                type="email"
                autoComplete="email"
                placeholder={t(($) => $.common.email_placeholder)}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-password">{t(($) => $.common.password)}</Label>
              <Input
                id="signup-password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-confirm-password">
                {t(($) => $.common.confirm_password)}
              </Label>
              <Input
                id="signup-confirm-password"
                type="password"
                autoComplete="new-password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                required
              />
            </div>
            {signupTotpRequired && (
              <div className="space-y-2">
                <Label htmlFor="signup-totp">{t(($) => $.common.totp)}</Label>
                <Input
                  id="signup-totp"
                  type="text"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]*"
                  maxLength={6}
                  placeholder={t(($) => $.common.totp_placeholder)}
                  value={totp}
                  onChange={(e) => setTotp(e.target.value)}
                  required
                />
                <p className="text-body text-muted-foreground">
                  {t(($) => $.signup.totp_description)}
                </p>
              </div>
            )}
            {error && (
              <p className="text-body text-destructive">{error}</p>
            )}
          </form>
        </CardContent>
        <CardFooter className="flex flex-col gap-3">
          <Button
            type="submit"
            form="signup-form"
            className="w-full"
            size="lg"
            disabled={
              loading ||
              !username ||
              !password ||
              !confirmPassword ||
              (signupTotpRequired && !totp.trim())
            }
          >
            {loading
              ? t(($) => $.signup.creating)
              : t(($) => $.signup.create_account)}
          </Button>
          <p className="text-body text-muted-foreground">
            {t(($) => $.signup.have_account)}{" "}
            <a
              href={paths.login()}
              className="font-medium text-foreground underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground/70"
            >
              {t(($) => $.signup.sign_in)}
            </a>
          </p>
        </CardFooter>
      </Card>
    </div>
  );
}
