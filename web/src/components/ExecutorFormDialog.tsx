import { useId, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createWorker, updateWorker } from "../api/client";
import type { Worker } from "../api/types";
import { Dialog } from "./Dialog";
import { useToast } from "./Toast";

function parseLabels(value: string): string[] {
  return Array.from(new Set(value.split(",").map((label) => label.trim()).filter(Boolean)));
}

export function ExecutorFormDialog({
  worker,
  onClose,
  onSaved,
}: {
  worker?: Worker;
  onClose: () => void;
  onSaved: (worker: Worker) => void;
}) {
  const editing = Boolean(worker);
  const queryClient = useQueryClient();
  const toast = useToast();
  const nameId = useId();
  const labelsId = useId();
  const concurrencyId = useId();
  const descriptionId = useId();
  const [name, setName] = useState(worker?.name ?? "");
  const [labels, setLabels] = useState((worker?.labels ?? ["linux"]).join(", "));
  const [concurrency, setConcurrency] = useState(String(worker?.concurrencyLimit ?? 16));
  const [description, setDescription] = useState(worker?.description ?? "");
  const [error, setError] = useState("");

  const save = useMutation({
    mutationFn: () => {
      const limit = Number(concurrency);
      if (!name.trim()) throw new Error("执行器名称不能为空。");
      if ([...name.trim()].length > 200) throw new Error("执行器名称不能超过 200 个字符。");
      if (!Number.isInteger(limit) || limit < 1 || limit > 20) {
        throw new Error("并发上限必须是 1 到 20 之间的整数。");
      }
      if ([...description].length > 2000) throw new Error("描述不能超过 2000 个字符。");
      const input = {
        name: name.trim(),
        description,
        labels: parseLabels(labels),
        concurrencyLimit: limit,
      };
      return worker ? updateWorker(worker.id, input) : createWorker(input);
    },
    onSuccess: (saved) => {
      toast.show(editing ? "执行器已更新。" : "执行器已创建。", "success");
      void queryClient.invalidateQueries({ queryKey: ["workers"] });
      if (worker) void queryClient.invalidateQueries({ queryKey: ["worker", worker.id] });
      onSaved(saved);
    },
    onError: (mutationError) => setError(mutationError.message),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError("");
    save.mutate();
  };

  return (
    <Dialog title={editing ? "编辑执行器" : "新建执行器"} onClose={onClose}>
      <form onSubmit={submit}>
        <div className="form-grid">
          <div className="field span-2">
            <label htmlFor={nameId}>名称</label>
            <input
              id={nameId}
              value={name}
              maxLength={200}
              autoComplete="off"
              placeholder="例如 linux-runner-01"
              onChange={(event) => setName(event.target.value)}
            />
            <span className="help">名称用于管理台识别，必须唯一，最多 200 个字符。</span>
          </div>
          <div className="field">
            <label htmlFor={concurrencyId}>并发上限</label>
            <input
              id={concurrencyId}
              type="number"
              min={1}
              max={20}
              value={concurrency}
              onChange={(event) => setConcurrency(event.target.value)}
            />
            <span className="help">1–20；同时也是领取任务时的上限。</span>
          </div>
          <div className="field">
            <label htmlFor={labelsId}>标签</label>
            <input
              id={labelsId}
              value={labels}
              autoComplete="off"
              placeholder="linux, cpu=8"
              onChange={(event) => setLabels(event.target.value)}
            />
            <span className="help">逗号分隔，与任务的 required labels 匹配。</span>
          </div>
          <div className="field span-2">
            <label htmlFor={descriptionId}>描述</label>
            <textarea
              id={descriptionId}
              rows={3}
              value={description}
              maxLength={2000}
              placeholder="可选，例如机房、用途或负责人"
              onChange={(event) => setDescription(event.target.value)}
            />
          </div>
        </div>
        <p className="modal-help">
          Worker 进程注册时会同步自身启动参数中的标签和并发上限。若要让管理台修改长期生效，请同时更新 Worker
          的启动配置。
        </p>
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}
        <div className="modal-actions">
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn primary" disabled={save.isPending}>
            {save.isPending ? "保存中…" : editing ? "保存修改" : "创建执行器"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
