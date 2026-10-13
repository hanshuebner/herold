/**
 * The app-actions group of the thread more-menu (REQ-APPS-01..03, 15).
 * One item per offered action, in capability order, with the app-action
 * icon, the localized label, the `<label> — <appName>` tooltip and the
 * busy state of a running invocation.
 */
import type { MoreMenuItem, MoreMenuSection } from '../mail/ThreadMoreMenu.svelte';
import AppActionIcon from './AppActionIcon.svelte';
import type { AppAction } from './app-actions';
import type { AppInvoker, InvokeTarget } from './app-invoke.svelte';

export const APP_ACTIONS_SECTION_ID = 'app-actions';

export function appActionTooltip(action: Pick<AppAction, 'label' | 'appName'>): string {
  return action.appName ? `${action.label} — ${action.appName}` : action.label;
}

export function appActionsSection(
  actions: readonly AppAction[],
  invoker: Pick<AppInvoker, 'invoke' | 'isBusy'>,
  target: InvokeTarget | null,
  sectionLabel: string,
): MoreMenuSection {
  const items: MoreMenuItem[] = target
    ? actions.map((action) => ({
        id: `app:${action.appId}:${action.extensionId}`,
        label: action.label,
        title: appActionTooltip(action),
        icon: AppActionIcon,
        iconProps: { name: action.icon },
        busy: invoker.isBusy(action, target.threadId),
        onclick: () => {
          void invoker.invoke(action, target);
        },
      }))
    : [];
  return { id: APP_ACTIONS_SECTION_ID, label: sectionLabel, items };
}
