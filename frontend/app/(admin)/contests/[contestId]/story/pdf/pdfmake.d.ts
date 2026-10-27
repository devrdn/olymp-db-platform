/**
 * The part of pdfmake 0.3 the story PDF uses. The package ships no types; the
 * browser build and the Node build both export one instance with this shape
 * (`setLocalAccessPolicy` only in Node).
 */
declare module "pdfmake" {
  export type Content = Record<string, unknown>;

  export interface OutputDocument {
    getBuffer(): Promise<Uint8Array>;
  }

  export interface PdfMake {
    virtualfs: { writeFileSync(name: string, content: Uint8Array): void };
    setFonts(fonts: Record<string, { normal: string; bold: string; italics: string; bolditalics: string }>): void;
    setUrlAccessPolicy(allow: (url: string) => boolean): void;
    setLocalAccessPolicy?(allow: (path: string) => boolean): void;
    createPdf(definition: Content): OutputDocument;
  }

  const pdfMake: PdfMake;
  export default pdfMake;
}
