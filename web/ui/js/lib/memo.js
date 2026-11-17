import { Component } from "preact";

function shallowEqual(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) {
    if (!Object.is(a[k], b[k])) return false;
  }
  return true;
}

export function memo(Render, compare) {
  return class Memoized extends Component {
    shouldComponentUpdate(nextProps) {
      return !(compare ? compare(this.props, nextProps) : shallowEqual(this.props, nextProps));
    }
    render(props) {
      return Render(props);
    }
  };
}
