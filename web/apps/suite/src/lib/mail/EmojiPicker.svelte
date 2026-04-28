<script lang="ts">
  /**
   * Minimal emoji picker for email reactions (REQ-MAIL-152).
   *
   * A small floating grid of common emoji. No native picker dependency.
   * Emits `select` when the user picks an emoji. The parent is responsible
   * for positioning and dismissal.
   */

  /**
   * The common reaction set — a GitHub/Slack-inspired shortlist that covers
   * the overwhelming majority of reaction use cases without UI clutter.
   *
   * These are user-visible data strings, not code/comment identifiers, so
   * the no-emoji-in-code rule does not apply here.
   */
  const EMOJI_SET = [
    '\u{1F44D}', // thumbs up
    '\u{1F44E}', // thumbs down
    '\u{2764}\u{FE0F}', // red heart
    '\u{1F602}', // face with tears of joy
    '\u{1F62E}', // face with open mouth (wow)
    '\u{1F64F}', // folded hands / pray
    '\u{1F440}', // eyes
    '\u{1F525}', // fire
    '\u{1F389}', // party popper
    '\u{2705}', // white check mark
    '\u{274C}', // cross mark
    '\u{1F44F}', // clapping hands
    '\u{1F914}', // thinking face
    '\u{1F973}', // partying face
    '\u{1F615}', // confused face
    '\u{1F64C}', // raising hands
    '\u{1F4AF}', // hundred points
    '\u{1F680}', // rocket
    '\u{1F4A1}', // light bulb
    '\u{1F3C6}', // trophy
    '\u{1F44B}', // waving hand
    '\u{1F4AA}', // flexed biceps
    '\u{1F90D}', // white heart
    '\u{1F4A9}', // pile of poo
    '\u{1F631}', // face screaming in fear
    '\u{1F923}', // rolling on floor laughing
    '\u{1F61C}', // winking face with tongue
    '\u{1F44C}', // ok hand
    '\u{270B}', // raised hand
    '\u{1F4DC}', // scroll
  ] as const;

  interface Props {
    onSelect: (emoji: string) => void;
    onClose: () => void;
  }
  let { onSelect, onClose }: Props = $props();

  function pick(emoji: string): void {
    onSelect(emoji);
    onClose();
  }

  function handleKeydown(ev: KeyboardEvent): void {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      ev.stopPropagation();
      onClose();
    }
  }
</script>

<div
  class="picker"
  role="dialog"
  aria-label="Pick a reaction emoji"
  tabindex="-1"
  onkeydown={handleKeydown}
>
  <div class="grid">
    {#each EMOJI_SET as emoji}
      <button
        type="button"
        class="emoji-btn"
        aria-label={emoji}
        onclick={() => pick(emoji)}
      >{emoji}</button>
    {/each}
  </div>
</div>

<style>
  .picker {
    display: inline-block;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
    padding: var(--spacing-03);
    z-index: 200;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(6, 1fr);
    gap: var(--spacing-01);
  }

  .emoji-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: var(--radius-md);
    font-size: 18px;
    line-height: 1;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .emoji-btn:hover {
    background: var(--layer-03);
  }

  .emoji-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
  }
</style>
