import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import ts from "@typescript/typescript6";

const input = JSON.parse(fs.readFileSync(0, "utf8"));
const root = path.resolve(input.root);
const allowed = new Map(input.files.map((value) => {
  const relative = value.replaceAll("\\", "/");
  return [path.resolve(root, relative), relative];
}));
const compilerOptions = {
  allowJs: true,
  checkJs: true,
  noEmit: true,
  noResolve: false,
  skipLibCheck: true,
  target: ts.ScriptTarget.ES2024,
  module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler,
  jsx: ts.JsxEmit.Preserve,
};
const defaultHost = ts.createCompilerHost(compilerOptions, true);
const typescriptLib = path.resolve(path.dirname(ts.getDefaultLibFilePath(compilerOptions)));
const inside = (candidate, base) => candidate === base || candidate.startsWith(`${base}${path.sep}`);
const readable = (candidate) => inside(path.resolve(candidate), root) || inside(path.resolve(candidate), typescriptLib);
const host = {
  ...defaultHost,
  fileExists: (candidate) => readable(candidate) && defaultHost.fileExists(candidate),
  readFile: (candidate) => readable(candidate) ? defaultHost.readFile(candidate) : undefined,
  realpath: (candidate) => path.resolve(candidate),
};
const program = ts.createProgram([...allowed.keys()].sort(), compilerOptions, host);
const checker = program.getTypeChecker();
const symbols = [];
const relations = [];
const diagnostics = [];
const symbolKeys = new Map();

function sourcePath(file) {
  return allowed.get(path.resolve(file.fileName));
}

function sourceRange(file, node) {
  const startOffset = node.getStart(file, false);
  const endOffset = node.getEnd();
  const start = file.getLineAndCharacterOfPosition(startOffset);
  const end = file.getLineAndCharacterOfPosition(endOffset);
  return {
    start: { line: start.line + 1, column: start.character + 1, offset: Buffer.byteLength(file.text.slice(0, startOffset), "utf8") },
    end: { line: end.line + 1, column: end.character + 1, offset: Buffer.byteLength(file.text.slice(0, endOffset), "utf8") },
  };
}

function declarationKind(node) {
  if (ts.isClassDeclaration(node)) return "class";
  if (ts.isInterfaceDeclaration(node)) return "interface";
  if (ts.isTypeAliasDeclaration(node)) return "type";
  if (ts.isEnumDeclaration(node)) return "enum";
  if (ts.isFunctionDeclaration(node) || ts.isArrowFunction(node) || ts.isFunctionExpression(node)) return "function";
  if (ts.isMethodDeclaration(node) || ts.isMethodSignature(node)) return "method";
  if (ts.isConstructorDeclaration(node)) return "constructor";
  if (ts.isVariableDeclaration(node)) return "variable";
  if (ts.isPropertyDeclaration(node) || ts.isPropertySignature(node)) return "property";
  if (ts.isModuleDeclaration(node)) return "namespace";
  return undefined;
}

function declarationName(node) {
  if (node.name && ts.isIdentifier(node.name)) return node.name.text;
  return undefined;
}

function isExported(node) {
  return Boolean(node.modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword || modifier.kind === ts.SyntaxKind.DefaultKeyword));
}

function qualifiedName(symbol, fallback) {
  if (!symbol) return fallback;
  return checker.getFullyQualifiedName(symbol).replace(/^".*"\./, "");
}

function symbolSignature(symbol, node) {
  if (!symbol) return "";
  const type = checker.getTypeOfSymbolAtLocation(symbol, node);
  const signatures = checker.getSignaturesOfType(type, ts.SignatureKind.Call);
  if (signatures.length > 0) return checker.signatureToString(signatures[0], node, ts.TypeFormatFlags.NoTruncation);
  return checker.typeToString(type, node, ts.TypeFormatFlags.NoTruncation);
}

function classify(node, name, filePath) {
  const decorators = ts.canHaveDecorators(node) ? ts.getDecorators(node) ?? [] : [];
  const decoratorNames = decorators.map((value) => value.expression.getText()).join(".").toLowerCase();
  const test = /(?:^|\/)(?:test|tests|__tests__)(?:\/|$)|\.(?:test|spec)\.[cm]?[jt]sx?$/.test(filePath) || /^(?:test|it|describe)$/.test(name);
  const route = /(?:route|get|post|put|patch|delete|controller)/.test(decoratorNames);
  const schema = /(?:schema|model|dto)$/.test(name.toLowerCase()) || /(?:entity|column)/.test(decoratorNames);
  return { test, route, schema };
}

function visitDeclarations(file, node) {
  const kind = declarationKind(node);
  const name = declarationName(node);
  if (kind && name) {
    const symbol = checker.getSymbolAtLocation(node.name);
    const range = sourceRange(file, node);
    const qname = qualifiedName(symbol, `${sourcePath(file)}:${name}`);
    const key = `typescript:${sourcePath(file)}:${range.start.offset}:${kind}:${qname}`;
    const flags = classify(node, name, sourcePath(file));
    symbols.push({
      key, path: sourcePath(file), language: /\.tsx?$/.test(file.fileName) ? "typescript" : "javascript",
      kind, name, qualified_name: qname, signature: symbolSignature(symbol, node),
      declaration: range, exported: isExported(node), ...flags,
    });
    if (symbol) symbolKeys.set(symbol, key);
  }
  ts.forEachChild(node, (child) => visitDeclarations(file, child));
}

for (const file of program.getSourceFiles()) {
  if (sourcePath(file)) visitDeclarations(file, file);
}

function nearestSymbol(node) {
  let current = node;
  while (current) {
    if (current.name) {
      const symbol = checker.getSymbolAtLocation(current.name);
      if (symbolKeys.has(symbol)) return symbolKeys.get(symbol);
    }
    current = current.parent;
  }
  return undefined;
}

function targetFor(symbol) {
  if (symbol && symbolKeys.has(symbol)) return { target_key: symbolKeys.get(symbol) };
  if (symbol) return { external_name: qualifiedName(symbol, symbol.getName()) };
  return undefined;
}

function addRelation(file, node, kind, target, unresolved) {
  const value = {
    source_path: sourcePath(file), kind, evidence_class: "confirmed_static",
    locator: sourceRange(file, node),
  };
  const source = nearestSymbol(node);
  if (source) value.source_key = source;
  if (target) Object.assign(value, target);
  else value.unresolved_name = unresolved || node.getText(file).slice(0, 2048);
  relations.push(value);
}

function visitRelations(file, node) {
  if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
    const moduleName = node.moduleSpecifier.text;
    const resolved = ts.resolveModuleName(moduleName, file.fileName, compilerOptions, host).resolvedModule;
    const targetPath = resolved && allowed.get(path.resolve(resolved.resolvedFileName));
    addRelation(file, node.moduleSpecifier, "imports", targetPath ? { target_path: targetPath } : { external_name: moduleName });
  }
  if (ts.isCallExpression(node)) {
    addRelation(file, node.expression, "calls", targetFor(checker.getSymbolAtLocation(node.expression)));
  } else if (ts.isIdentifier(node) && !ts.isDeclarationName(node)) {
    const target = targetFor(checker.getSymbolAtLocation(node));
    if (target && nearestSymbol(node)) addRelation(file, node, "references", target);
  }
  if (ts.isHeritageClause(node)) {
    const kind = node.token === ts.SyntaxKind.ImplementsKeyword ? "implements" : "extends";
    for (const type of node.types) addRelation(file, type, kind, targetFor(checker.getSymbolAtLocation(type.expression)));
  }
  ts.forEachChild(node, (child) => visitRelations(file, child));
}

for (const file of program.getSourceFiles()) {
  if (sourcePath(file)) visitRelations(file, file);
}

for (const diagnostic of ts.getPreEmitDiagnostics(program)) {
  if (!diagnostic.file || !sourcePath(diagnostic.file)) continue;
  diagnostics.push({
    path: sourcePath(diagnostic.file),
    severity: diagnostic.category === ts.DiagnosticCategory.Error ? "error" : diagnostic.category === ts.DiagnosticCategory.Warning ? "warning" : "info",
    code: `ts/${diagnostic.code}`,
  });
}

process.stdout.write(JSON.stringify({ symbols, relations, diagnostics }));
