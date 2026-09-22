import assert from "node:assert/strict";
import test from "node:test";
import { dismissesMenu } from "./menuDismiss.ts";

// A stand-in for a DOM node: it contains the nodes it was given.
const node = (children = []) => {
  const self = { children, contains: (other) => other === self || children.some((child) => child.contains(other)) };
  return self;
};

test("a click on the button or inside the menu keeps it open", () => {
  const item = node();
  const menu = node([item]);
  const button = node();
  const anchor = node([button]);
  assert.equal(dismissesMenu(item, [anchor, menu]), false);
  assert.equal(dismissesMenu(menu, [anchor, menu]), false);
  assert.equal(dismissesMenu(button, [anchor, menu]), false);
});

test("a click elsewhere closes it, and so does one with no target", () => {
  const menu = node();
  const anchor = node();
  assert.equal(dismissesMenu(node(), [anchor, menu]), true);
  assert.equal(dismissesMenu(null, [anchor, menu]), true);
  // A portal menu that is not mounted yet must not keep the menu open.
  assert.equal(dismissesMenu(node(), [anchor, null]), true);
});
