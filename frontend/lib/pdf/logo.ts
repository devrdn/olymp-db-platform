import { COLOR } from "./theme";

/** A logo drawn to a JPEG, with its size in pixels. */
export type LogoImage = { src: string; width: number; height: number };

/**
 * The longer side of the drawn logo, in pixels: sharp at the size it prints
 * even at 600 dpi, and small enough to embed cheaply.
 */
const RASTER = 512;

const JPEG_QUALITY = 0.95;

function load(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error("the logo could not be loaded"));
    image.src = src;
  });
}

/**
 * The installation's uploaded logo as a JPEG. The PDF library reads PNG and
 * JPEG only, and an installation may upload another format, so the picture is
 * drawn on a canvas and read back. A JPEG because the library embeds one as
 * it is, while a PNG with transparency it would decode; transparency becomes
 * the white of the paper.
 *
 * Rejects when the picture cannot be loaded or drawn; the caller decides that
 * a PDF without a logo is better than none.
 */
export async function loadLogo(href: string): Promise<LogoImage> {
  const image = await load(href);

  // A vector picture with no size of its own reports none.
  const aspect = image.naturalWidth > 0 && image.naturalHeight > 0 ? image.naturalWidth / image.naturalHeight : 1;
  const width = Math.round(aspect >= 1 ? RASTER : RASTER * aspect);
  const height = Math.round(aspect >= 1 ? RASTER / aspect : RASTER);

  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const context = canvas.getContext("2d");
  if (!context) throw new Error("no canvas to draw the logo on");

  context.fillStyle = COLOR.paper;
  context.fillRect(0, 0, width, height);
  context.drawImage(image, 0, 0, width, height);
  return { src: canvas.toDataURL("image/jpeg", JPEG_QUALITY), width, height };
}
