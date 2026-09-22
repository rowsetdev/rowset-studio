/**
 * Whether a pointer event outside a menu should close it.
 *
 * Menus that render through a portal are not inside their button's element,
 * so a "click outside" check against the button alone also matches the menu's
 * own items: the menu unmounts on mousedown and the item never receives its
 * click. Pass every element that belongs to the menu.
 */
export function dismissesMenu(target: Node | null, parts: (Node | null | undefined)[]): boolean {
  if (!target) return true;
  return !parts.some((part) => part && (part === target || part.contains(target)));
}
