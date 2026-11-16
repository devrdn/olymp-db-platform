/**
 * The product's marks, copied shape for shape from the owner's design canvas.
 *
 * Each shape names its paint. `ink` and `chalk` are the black and the cream
 * patches, which trade places on a dark ground; `ink-line` is a stroke in ink;
 * every other paint is a key of PAINT and is the same in both themes. Plain
 * JavaScript, so the icon script can import it as well as the app.
 *
 * @typedef {{ el: "path" | "polygon" | "rect" | "ellipse", attrs: Record<string, string>, paint: string }} Shape
 * @typedef {{ viewBox: string, shapes: Shape[] }} Mark
 */

/** The colours that do not change with the theme. */
export const PAINT = {
  terracotta: "#E0805F",
  green: "#6B9E57",
  mustard: "#E8B24A",
  red: "#C8392E",
  pupil: "#1E1A18",
};

/** The width of the stroke that `ink-line` draws, in the mark's own units. */
export const LINE_WIDTH = 1.2;

/**
 * The logo: the cat on its pedestal. Its smallest size is 32 px high.
 *
 * @type {Mark}
 */
export const CAT = {
  viewBox: "16 8 90 116",
  shapes: [
    { el: "path", attrs: { d: "M28,92 A32,7 0 0 0 92,92 L92,102 A32,7 0 0 1 28,102 Z" }, paint: "green" },
    { el: "path", attrs: { d: "M28,105 A32,7 0 0 0 92,105 L92,114 A32,7 0 0 1 28,114 Z" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "36.00,99.53 38.10,101.63 36.00,103.73 33.90,101.63" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "48.00,101.39 50.10,103.49 48.00,105.59 45.90,103.49" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "60.00,101.90 62.10,104.00 60.00,106.10 57.90,104.00" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "72.00,101.39 74.10,103.49 72.00,105.59 69.90,103.49" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "84.00,99.53 86.10,101.63 84.00,103.73 81.90,101.63" }, paint: "chalk" },
    { el: "ellipse", attrs: { cx: "60", cy: "92", rx: "32", ry: "7" }, paint: "ink" },
    { el: "polygon", attrs: { points: "37,88 31,85 29,91 36,94" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "31,85 26,79 22,84 29,91" }, paint: "ink" },
    { el: "polygon", attrs: { points: "26,79 25,71 19,72 22,84" }, paint: "green" },
    { el: "polygon", attrs: { points: "25,71 27,63 21,61 19,72" }, paint: "mustard" },
    { el: "polygon", attrs: { points: "27,63 25,56 21,61" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "36,94 37,78 46,62 55,47 70,44 74,58 73,94" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "36,94 37,78 46,62 52,72 48,94" }, paint: "ink" },
    { el: "polygon", attrs: { points: "48,94 52,82 61,86 59,94" }, paint: "green" },
    { el: "polygon", attrs: { points: "62,48 70,46 71,59 66,66 62,58" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "63,90 73,89 75,94 62,94" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "54,46 53,30 55,14 62,24 70,12 73,24 86,28 82,36 70,44" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "54,46 53,30 55,14 62,24 64,34 58,44" }, paint: "ink" },
    { el: "polygon", attrs: { points: "70,12 73,24 68,21" }, paint: "mustard" },
    { el: "path", attrs: { d: "M67,27 Q71,23.5 75,26.5 Q71,29.5 67,27Z" }, paint: "mustard" },
    { el: "rect", attrs: { x: "70.6", y: "24.6", width: "1.1", height: "4" }, paint: "pupil" },
    { el: "polygon", attrs: { points: "83.5,27.6 86.5,28.2 84.6,30.6" }, paint: "red" },
    { el: "polygon", attrs: { points: "54,45 70,42 71,46 55,49" }, paint: "red" },
    { el: "polygon", attrs: { points: "66.0,51.2 66.7,53.3 68.7,52.3 67.7,54.3 69.8,55.0 67.7,55.7 68.7,57.7 66.7,56.7 66.0,58.8 65.3,56.7 63.3,57.7 64.3,55.7 62.2,55.0 64.3,54.3 63.3,52.3 65.3,53.3" }, paint: "red" },
    { el: "path", attrs: { d: "M80,32 L102,27 M80,34 L103,34" }, paint: "ink-line" },
  ],
};

/**
 * A mark as a standalone SVG document, its theme patches in `scheme`'s
 * colours, for a renderer that has no stylesheet (a PDF).
 *
 * @param {Mark} mark
 * @param {{ ink: string, chalk: string }} scheme
 * @returns {string}
 */
export function markSvg(mark, scheme) {
  const paint = (name) => {
    if (name === "ink-line") {
      return `fill="none" stroke="${scheme.ink}" stroke-width="${LINE_WIDTH}" stroke-linecap="round"`;
    }
    return `fill="${name === "ink" ? scheme.ink : name === "chalk" ? scheme.chalk : PAINT[name]}"`;
  };
  const shapes = mark.shapes.map(({ el, attrs, paint: name }) => {
    const attributes = Object.entries(attrs).map(([key, value]) => `${key}="${value}"`);
    return `<${el} ${attributes.join(" ")} ${paint(name)}/>`;
  });
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${mark.viewBox}">${shapes.join("")}</svg>`;
}
