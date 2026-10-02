"use client";
/* The shared card is also used by the HTML preview and ships its compact
 * copy with the protocol until the product-wide locale bundle adds ask keys. */
/* eslint-disable i18next/no-literal-string */

import { useMemo, useState } from "react";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";

export type AskOption = { id: string; label: string; recommended?: boolean };
export type AskQuestion = { text: string; options: AskOption[] };
export type AskOptionsCardProps = {
  title: string;
  questions: AskQuestion[];
  mode?: "needs_you" | "side_question";
  disabled?: boolean;
  onSubmit: (answers: Record<string, string>) => void | Promise<void>;
};

/** Shared answer card for chat, issue activity and inbox surfaces. */
export function AskOptionsCard({ title, questions, mode = "needs_you", disabled, onSubmit }: AskOptionsCardProps) {
  const [current, setCurrent] = useState(0);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [otherValues, setOtherValues] = useState<Record<string, string>>({});
  const [otherSelected, setOtherSelected] = useState<Set<string>>(() => new Set());
  const [submitting, setSubmitting] = useState(false);
  const question = questions[current];
  const currentKey = String(current);
  const selected = otherSelected.has(currentKey) ? "other" : answers[currentKey] ?? "";
  const canSubmit = questions.every((questionItem, index) => {
    const key = String(index);
    return Boolean(answers[key]) || (!otherSelected.has(key) && questionItem.options.length > 0);
  });
  const recommended = useMemo(() => question?.options.find((option) => option.recommended), [question]);
  if (!question) return null;

  const choose = (value: string) => {
    if (value === "other") {
      setOtherSelected((prev) => new Set(prev).add(currentKey));
      setAnswers((prev) => {
        const next = { ...prev };
        delete next[currentKey];
        return next;
      });
    } else {
      setOtherSelected((prev) => {
        const next = new Set(prev);
        next.delete(currentKey);
        return next;
      });
      setAnswers((prev) => ({ ...prev, [currentKey]: value }));
    }
    if (current < questions.length - 1) setTimeout(() => setCurrent((value) => value + 1), 120);
  };
  const submit = async () => {
    if (!canSubmit || submitting) return;
    setSubmitting(true);
    try { await onSubmit(answers); } finally { setSubmitting(false); }
  };

  return (
    <Card size="sm" className="w-full max-w-xl">
      <CardHeader className="border-b border-surface-border">
        <div className="flex items-center justify-between gap-3">
          <CardTitle>{title}</CardTitle>
          {mode === "side_question" ? <span className="text-caption text-muted-foreground">顺手问</span> : <span className="text-caption text-brand">需要你</span>}
        </div>
        <CardDescription>请选择一个选项，所有问题回答后提交即可唤醒提问的智能体。</CardDescription>
        {questions.length > 1 && <div className="flex gap-1 pt-2" aria-label="问题进度">{questions.map((_, index) => <button key={index} type="button" aria-label={`第 ${index + 1} 题`} onClick={() => setCurrent(index)} className={`h-1.5 flex-1 rounded-full ${index === current ? "bg-brand" : answers[String(index)] ? "bg-brand/45" : "bg-surface-border"}`} />)}</div>}
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        <div className="flex items-start justify-between gap-3"><p className="font-medium">{questions.length > 1 && <span className="mr-2 text-muted-foreground">{current + 1}/{questions.length}</span>}{question.text}</p>{recommended && <span className="rounded-full bg-brand/10 px-2 py-0.5 text-caption text-brand">推荐</span>}</div>
        <div className="grid gap-2" role="radiogroup" aria-label={question.text}>
          {question.options.map((option) => <button key={option.id} type="button" role="radio" aria-checked={selected === option.id} onClick={() => choose(option.id)} className={`flex items-center justify-between rounded-lg border px-3 py-2.5 text-left text-body transition-colors ${selected === option.id ? "border-brand bg-brand/10" : "border-surface-border hover:bg-surface-hover"}`}><span>{option.label}</span>{option.recommended && <span className="text-caption text-muted-foreground">推荐</span>}</button>)}
          <button type="button" role="radio" aria-checked={selected === "other"} onClick={() => choose("other")} className={`rounded-lg border px-3 py-2.5 text-left text-body ${selected === "other" ? "border-brand bg-brand/10" : "border-surface-border hover:bg-surface-hover"}`}>其他，我自己说</button>
          {selected === "other" && <Textarea value={otherValues[currentKey] ?? ""} onChange={(event) => { const value = event.target.value; setOtherValues((prev) => ({ ...prev, [currentKey]: value })); setAnswers((prev) => ({ ...prev, [currentKey]: value })); }} placeholder="输入你的答案" autoFocus />}
        </div>
      </CardContent>
      <CardFooter className="justify-between gap-2"><Button type="button" variant="ghost" size="sm" disabled={current === 0} onClick={() => setCurrent((value) => value - 1)}>上一题</Button><Button type="button" size="sm" disabled={disabled || !canSubmit || submitting} onClick={submit}>{submitting ? "提交中…" : "提交回答"}</Button></CardFooter>
    </Card>
  );
}
