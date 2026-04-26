/**
 * Help-overlay state per docs/requirements/10-keyboard.md REQ-KEY-01:
 * `?` opens an overlay listing every active binding. Pressing `?` again
 * (or Escape) closes it.
 */

class Help {
  isOpen = $state(false);
  toggle(): void {
    this.isOpen = !this.isOpen;
  }
  close(): void {
    this.isOpen = false;
  }
}

export const help = new Help();
