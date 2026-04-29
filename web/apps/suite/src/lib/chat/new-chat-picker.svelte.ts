/**
 * Singleton store for the new-chat picker modal.
 *
 * Callers open the picker via newChatPicker.open({ mode: 'dm' | 'space' }).
 * The NewChatPicker component reads `newChatPicker.pending` and renders
 * when it is non-null.
 *
 * REQ-CHAT-01a..d, REQ-CHAT-02a, REQ-CHAT-15.
 */

export type PickerMode = 'dm' | 'space';

export interface PickerRequest {
  mode: PickerMode;
}

class NewChatPickerStore {
  pending = $state<PickerRequest | null>(null);

  open(req: PickerRequest): void {
    this.pending = req;
  }

  close(): void {
    this.pending = null;
  }
}

export const newChatPicker = new NewChatPickerStore();
