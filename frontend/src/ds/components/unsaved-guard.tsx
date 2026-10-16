import { useBlocker } from "@tanstack/react-router";
import { Button } from "./button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
} from "./dialog";

export interface UnsavedGuardProps {
  /* Block router navigation while true; tab close and refresh get the
   * browser's native prompt. */
  dirty: boolean;
  saving?: boolean;
  /* Commit the pending edits; navigation continues once it resolves
   * and stays put if it rejects. */
  onSave: () => void | Promise<void>;
  onDiscard: () => void;
}

/* SaveBar's companion: intercepts leaving the page while edits are
 * pending and makes the choice explicit. Closing the dialog keeps
 * the user editing; nothing is discarded by reflex. */
export function UnsavedGuard({
  dirty,
  saving = false,
  onSave,
  onDiscard,
}: UnsavedGuardProps) {
  const { proceed, reset, status } = useBlocker({
    shouldBlockFn: () => dirty,
    withResolver: true,
    enableBeforeUnload: () => dirty,
  });

  return (
    <UnsavedDialog
      open={status === "blocked"}
      saving={saving}
      onKeepEditing={() => reset?.()}
      onDiscard={() => {
        onDiscard();
        proceed?.();
      }}
      onSave={() => {
        void Promise.resolve(onSave()).then(
          () => proceed?.(),
          () => reset?.(),
        );
      }}
    />
  );
}

export interface UnsavedDialogProps {
  open: boolean;
  saving?: boolean;
  onKeepEditing: () => void;
  onDiscard: () => void;
  onSave: () => void;
}

/* The choice UnsavedGuard offers, for a way out the router does not
 * see: a step inside a flow, or an action that leaves once it is done. */
export function UnsavedDialog({
  open,
  saving = false,
  onKeepEditing,
  onDiscard,
  onSave,
}: UnsavedDialogProps) {
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onKeepEditing();
      }}
    >
      <DialogContent>
        <DialogTitle>Unsaved changes</DialogTitle>
        <DialogDescription>
          Save or discard your changes before leaving.
        </DialogDescription>
        <DialogFooter>
          <Button variant="ghost" disabled={saving} onClick={onKeepEditing}>
            Keep editing
          </Button>
          <Button variant="neutral" disabled={saving} onClick={onDiscard}>
            Discard and leave
          </Button>
          <Button variant="primary" loading={saving} onClick={onSave}>
            Save and leave
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
